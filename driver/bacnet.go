package driver

import (
	"context"
	"fmt"
	"github.com/patrickmn/go-cache"
	"github.com/thingio/edge-device-std/errors"
	"github.com/thingio/edge-device-std/logger"
	"github.com/thingio/edge-device-std/models"
	"github.com/thingio/edge-device-std/operations"
	"github.com/thingio/edge-randnum-driver/bacnet"
	"net"
	"strconv"
	"sync"
	"time"
)

type ObjectID uint32

const (
	OID_ANY ObjectID = 0x023fffff
)

var (
	OID_MAP = map[string]ObjectID{
		"OID_ANY": OID_ANY,
	}
)

type PropertyID byte

const (
	PID_OID                           PropertyID = 0x75
	PID_VENDOR_NUMBER                 PropertyID = 0x78
	PID_VENDOR_NAME                   PropertyID = 0x79
	PID_FIRMWARE_REVISION             PropertyID = 0x2c
	PID_APPLICATION_SOFTWARE_REVISION PropertyID = 0x0c
	PID_OBJECT_NAME                   PropertyID = 0x4d
	PID_MODEL_NAME                    PropertyID = 0x46
	PID_DESCRIPTION                   PropertyID = 0x1c
	PID_LOCATION                      PropertyID = 0x3a
)

var (
	PID_MAP = map[string]PropertyID{
		"PID_OID":                           PID_OID,
		"PID_VENDOR_NUMBER":                 PID_VENDOR_NUMBER,
		"PID_VENDOR_NAME":                   PID_VENDOR_NAME,
		"PID_FIRMWARE_REVISION":             PID_FIRMWARE_REVISION,
		"PID_APPLICATION_SOFTWARE_REVISION": PID_APPLICATION_SOFTWARE_REVISION,
		"PID_OBJECT_NAME":                   PID_OBJECT_NAME,
		"PID_MODEL_NAME":                    PID_MODEL_NAME,
		"PID_DESCRIPTION":                   PID_DESCRIPTION,
		"PID_LOCATION":                      PID_LOCATION,
	}
)

const (
	OID = "OID"
	PID = "PID"
)

type bacnetDriver struct {
	*bacnetDeviceConf
	ip        string
	port      int
	timeoutMS int
	conn      net.Conn // tcp connection
	closed    bool
	stop      chan bool
	lock      sync.Mutex
	once      sync.Once // ensure there is only one reconnect goroutine
}

type bacnetDeviceConf struct {
	*bacnet.Log
}

type bacnetTwin struct {
	*bacnetDriver

	product *models.Product
	device  *models.Device

	watchSchedulers map[time.Duration][]*models.ProductProperty          // for property's watching
	properties      map[models.ProductPropertyID]*models.ProductProperty // for property's reading
	propertyCache   *cache.Cache                                         // for property's reading
	events          map[models.ProductEventID]*models.ProductEvent       // for event's subscribing

	stopped chan bool

	lg *logger.Logger
}

func NewBacnetDriver(device *models.Device) *bacnetDriver {
	driver := &bacnetDriver{
		bacnetDeviceConf: &bacnetDeviceConf{&bacnet.Log{}},
		conn:             nil,
		stop:             make(chan bool),
		closed:           false,
		lock:             sync.Mutex{},
		once:             sync.Once{},
	}
	for k, v := range device.DeviceProps {
		switch k {
		case "ip":
			driver.ip = v
		case "port":
			port, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil
			}
			driver.port = int(port)
		case "timeout":
			t, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil
			}
			driver.timeoutMS = int(t)
		}
	}
	if driver.ip == "" || driver.port == 0 || driver.timeoutMS == 0 {
		return nil
	}
	return driver
}

func (d *bacnetDriver) parseBacnetDeviceConf() error {
	err := d.bacnetDeviceConf.Log.QueryDeviceID(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryVendorNumber(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryVendorName(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryFirmwareRevision(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryApplicationSoftwareRevision(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryObjectName(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryModelName(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryDescription(d.conn)
	if err != nil {
		return err
	}
	err = d.bacnetDeviceConf.Log.QueryLocation(d.conn)
	if err != nil {
		return err
	}
	return nil
}

func NewBacnetTwin(product *models.Product, device *models.Device) (models.DeviceTwin, error) {
	if product == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Product is nil", nil)
	}
	if device == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Device is nil", nil)
	}
	driver := NewBacnetDriver(device)
	if driver == nil {
		return nil, errors.NewCommonEdgeError(errors.DeviceTwin, "Driver is nil", nil)
	}
	twin := &bacnetTwin{
		bacnetDriver:    driver,
		product:         product,
		device:          device,
		stopped:         make(chan bool),
		watchSchedulers: make(map[time.Duration][]*models.ProductProperty),
		properties:      make(map[models.ProductPropertyID]*models.ProductProperty),
		propertyCache:   cache.New(30*time.Minute, 30*time.Minute),
		events:          make(map[models.ProductEventID]*models.ProductEvent),
	}
	return twin, nil
}

func (m *bacnetTwin) Initialize(lg *logger.Logger) error {
	m.lg = lg
	for _, property := range m.product.Properties {
		if property.ReportMode == operations.DeviceDataReportModePeriodical {
			duration, err := time.ParseDuration(property.Interval)
			if err != nil {
				return errors.NewCommonEdgeError(errors.Internal, fmt.Sprintf("invalid interval format: %s", property.Interval), nil)
			}

			_, ok := m.watchSchedulers[duration]
			if !ok {
				m.watchSchedulers[duration] = make([]*models.ProductProperty, 0)
			}
			m.watchSchedulers[duration] = append(m.watchSchedulers[duration], property)
		}

		m.properties[property.Id] = property
	}
	for _, event := range m.product.Events {
		m.events[event.Id] = event
	}

	m.lg.Info("success to initialize the modbus device connector")
	return nil
}

func (m *bacnetTwin) Start(ctx context.Context) error {
	if m.closed == true {
		m.lg.Error("bacnetTwin.Start Error: obj has been stopped")
		return nil
	}
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", m.ip, m.port))
	if err != nil {
		m.lg.Errorf("bacnetTwin.Start Error: illegal addr: %+v, err: %+v", addr, err)
		return err
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	conn, err := net.DialTimeout("udp", addr.String(), time.Duration(m.timeoutMS))
	if err != nil {
		m.lg.Errorf("bacnetTwin.Start Error: udp Connection addr: %+v, err: %+v", addr, err)
		return err
	}
	m.conn = conn
	m.lg.Info("success to start the bacnet device connector")
	err = m.bacnetDriver.parseBacnetDeviceConf()
	if err != nil {
		m.lg.Errorf("bacnetTwin.parseBacnetDeviceConf Error: err: %+v", err)
	}
	go m.once.Do(m.AutoReconnect)
	return nil
}

func (m *bacnetTwin) AutoReconnect() {
	hbTicker := time.NewTicker(time.Second * 10)
	for {
		select {
		case <-hbTicker.C:
			continue
		case <-m.stop:
			hbTicker.Stop()
			close(m.stop)
			m.closed = true
			return
		}
	}
}

func (m *bacnetTwin) Stop(force bool) error {
	defer func() {
		close(m.stopped)
	}()
	m.bacnetDriver.closed = true
	m.bacnetDriver.stop <- true
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			m.lg.Info("Stop BacnetTwin TCPDriver Error: %s:%s, err: %+v", m.ip, m.port, err)
			return err
		}
	}
	m.stopped <- true
	return nil
}

func (m *bacnetTwin) HealthCheck() (*models.DeviceStatus, error) {
	return nil, nil
}

func (m *bacnetTwin) Read(propertyID models.ProductPropertyID) (map[models.ProductPropertyID]*models.DeviceData, error) {
	read := func(id models.ProductPropertyID) (map[models.ProductPropertyID]*models.DeviceData, error) {
		res := make(map[models.ProductPropertyID]*models.DeviceData)
		property, ok := m.properties[id]
		if !ok {
			return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
		}
		m.lock.Lock()
		body, err, _ := bacnet.SendReadProperty(m.Log, m.conn, bacnet.ObjectID(OID_MAP[property.AuxProps[OID]]), bacnet.PropertyID(PID_MAP[property.AuxProps[PID]]))
		m.lock.Unlock()
		if err != nil {
			m.lg.Errorf("bacnet HardRead Error: propertyID %+v, error %+v", id, err)
			return res, errors.NewCommonEdgeError(errors.BadRequest, "read bacnet TCP device failed", nil)
		}
		if err != nil {
			return res, err
		}
		value := &models.DeviceData{
			Name:  property.Name,
			Type:  property.FieldType,
			Value: body,
			Ts:    time.Time{},
		}
		m.propertyCache.Set(property.Id, value, 30*time.Minute)
		res[id] = value
		return res, nil
	}
	values := make(map[models.ProductPropertyID]*models.DeviceData)
	if propertyID == models.DeviceDataMultiPropsID {
		for _, property := range m.properties {
			v, err := read(property.Id)
			if err != nil {
				m.lg.Infof("bacnetTwin.propertyCache.Get Fail: property = %+v", property)
				return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
			}
			for i, j := range v {
				values[i] = j
			}
		}
	} else {
		v, err := read(propertyID)
		if err != nil {
			m.lg.Infof("bacnetTwin Read Fail: property = %+v", propertyID)
			return nil, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", propertyID), nil)
		}
		for i, j := range v {
			values[i] = j
		}
	}

	return values, nil
}

func (m *bacnetTwin) Write(propertyID models.ProductPropertyID, values map[models.ProductPropertyID]*models.DeviceData) error {
	return nil
}

func (m *bacnetTwin) Subscribe(eventID models.ProductEventID, bus chan<- *models.DeviceDataWrapper) error {
	return nil
}

func (m *bacnetTwin) Call(methodID models.ProductMethodID, ins map[models.ProductPropertyID]*models.DeviceData) (outs map[models.ProductPropertyID]*models.DeviceData, err error) {
	return nil, nil
}
