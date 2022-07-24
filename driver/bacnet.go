package driver

import (
	"context"
	"fmt"
	bacnet "github.com/alexbeltran/gobacnet"
	bactype "github.com/alexbeltran/gobacnet/types"
	"github.com/thingio/edge-device-std/errors"
	"github.com/thingio/edge-device-std/logger"
	"github.com/thingio/edge-device-std/models"
	"github.com/thingio/edge-device-std/operations"
	"net"
	"strconv"
	"sync"
	"time"
)

type bacnetDriver struct {
	*bacnetDeviceConf
	pid    string // 设备所属产品的ID
	dvcID  string // 设备ID
	conn   net.Conn
	closed bool
	lock   sync.Mutex
}

type bacnetDeviceConf struct {
	*bacnet.Client
	ipaddr    string          // 设备bacnet Server地址
	dev       *bactype.Device //Client中用于数据交换的虚拟设备信息
	timeoutMS int
}

type bacnetTwin struct {
	*bacnetDriver

	product *models.Product
	device  *models.Device

	watchSchedulers map[time.Duration][]*models.ProductProperty          // for property's watching
	properties      map[models.ProductPropertyID]*models.ProductProperty // for property's reading
	events          map[models.ProductEventID]*models.ProductEvent       // for event's subscribing

	ctx     context.Context    // 标识当前driver生命周期的上下文
	cancel  context.CancelFunc // 用于关闭当前driver生命周期的方法句柄
	stopped chan bool
	lg      *logger.Logger
}

func NewBacnetDriver(device *models.Device) *bacnetDriver {
	driver := &bacnetDriver{
		bacnetDeviceConf: &bacnetDeviceConf{},
		conn:             nil,
		closed:           false,
		lock:             sync.Mutex{},
	}
	for k, v := range device.DeviceProps {
		switch k {
		case "ipaddr":
			driver.dev.IPAddr = v
		case "timeout":
			t, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return nil
			}
			driver.timeoutMS = int(t)
		}
	}
	if driver.ipaddr == "" || driver.timeoutMS == 0 {
		return nil
	}
	return driver
}

func (d *bacnetDriver) parseBacnetDeviceConf() error {
	err := d.Client.NewClient1()
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
	m.ctx, m.cancel = context.WithCancel(ctx)
	// 启动前清理残留对象
	if m.Client != nil {
		m.Client.Close()
	}
	if m.cancel != nil {
		m.cancel()
	}

	// 通过 cancel 与 context 控制定时读属性的协程
	m.ctx, m.cancel = context.WithCancel(context.Background())

	if m.closed == true {
		m.lg.Error("bacnetTwin.Start Error: obj has been stopped")
		return nil
	}
	m.lg.Info("success to start the bacnet device connector")
	err := m.bacnetDriver.parseBacnetDeviceConf()
	if err != nil {
		m.lg.Errorf("bacnetTwin.parseBacnetDeviceConf Error: err: %+v", err)
	}
	return nil
}

func (m *bacnetTwin) Stop(force bool) error {
	defer func() {
		close(m.stopped)
	}()
	if m.Client != nil {
		m.Client.Close()
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.bacnetDriver.closed = true
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
		ObjectTypeId, ObjectInstance, PropertyType, err := m.getPropertyInfo(id)
		if err != nil {
			m.lg.Errorf("can't get the info of property %s", id)
			return res, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("can't get the info of property %s", id), nil)
		}
		rp := bactype.ReadPropertyData{
			Object: bactype.Object{
				ID: bactype.ObjectID{
					Type:     bactype.ObjectType(ObjectTypeId),
					Instance: bactype.ObjectInstance(ObjectInstance),
				},
				Properties: []bactype.Property{
					{
						Type:       PropertyType,
						ArrayIndex: bacnet.ArrayAll,
					},
				},
			},
		}
		dev := bactype.Device{
			Addr: bactype.Address{
				IPaddr: m.dev.IPAddr,
			},
		}
		out, err := m.Client.ReadProperty(dev, rp)
		if err != nil {
			m.lg.Errorf("bacnet HardRead Error: propertyID %+v, error %+v", id, err)
			return res, errors.NewCommonEdgeError(errors.BadRequest, "read bacnet device failed", nil)
		}
		value := &models.DeviceData{
			Name:  property.Name,
			Type:  property.FieldType,
			Value: out.Object.Properties[0].Data,
			Ts:    time.Time{},
		}
		//m.propertyCache.Set(property.Id, value, 30*time.Minute)
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
	m.lg.Debugf("[%s]Write data :%+v", m.dvcID, values)
	if propertyID == models.DeviceDataMultiPropsID {
		for pid, data := range values {
			err := m.write(pid, data)
			if err != nil {
				return errors.NewCommonEdgeError(errors.DeviceTwin, fmt.Sprintf("unsuccess to write the Property [%s] ", pid), nil)
			}
		}
	} else {
		v, ok := values[propertyID]
		if !ok {
			return errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("can't find the data of the Property [%s] ", propertyID), nil)
		}
		err := m.write(propertyID, v)
		if err != nil {
			m.lg.Debugf("write error", err)
			return errors.NewCommonEdgeError(errors.DeviceTwin, fmt.Sprintf("unsuccess to write the Property [%s] ", propertyID), nil)
		}
	}
	m.lg.Infof("success to write the [%s] for the device[%s]", propertyID, m.device.ID)
	return nil
}

func (m *bacnetTwin) write(pid models.ProductPropertyID, data *models.DeviceData) error {
	ObjectTypeId, ObjectInstance, PropertyType, err := m.getPropertyInfo(pid)
	if err != nil {
		return err
	}
	rp := bactype.ReadPropertyData{
		Object: bactype.Object{
			ID: bactype.ObjectID{
				Type:     bactype.ObjectType(ObjectTypeId),
				Instance: bactype.ObjectInstance(ObjectInstance),
			},
			Properties: []bactype.Property{
				{
					Type:       PropertyType,
					ArrayIndex: bacnet.ArrayAll,
					Data:       data.Value,
				},
			},
		},
	}
	dev := bactype.Device{
		Addr: bactype.Address{
			IPaddr: m.dev.IPAddr,
		},
	}
	err = m.Client.WriteProperty(dev, rp, 0) //默认写优先级为0
	if err != nil {
		return err
	}
	return nil
}

func (m *bacnetTwin) Subscribe(eventID models.ProductEventID, bus chan<- *models.DeviceDataWrapper) error {
	// 获取事件配置
	ec, ok := m.events[eventID]
	if !ok {
		return errors.NewCommonEdgeError(errors.Driver, fmt.Sprintf("opcua event '%s' not found in %+v", eventID, m.events), nil)
	}
	propertyID := ec.Outs[0].Id
	property, ok := m.properties[propertyID]
	if !ok {
		return errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
	}
	ObjectTypeId, ObjectInstance, PropertyType, err := m.getPropertyInfo(propertyID)
	if err != nil {
		m.lg.Errorf("can't get the info of property %s", propertyID)
		return errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("can't get the info of property %s", propertyID), nil)
	}

	// COV订阅时间 若不存在则订阅时间为永久
	lifetime := 0
	value, ok := property.AuxProps["Lifetime"]
	if ok {
		lifetime, err = strconv.Atoi(value)
		if err != nil {
			m.lg.Errorf("wrong lifetime : ", property.AuxProps["Lifetime"])
			return errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the info of property lifetime %s is wrong", property.AuxProps["Lifetime"]), nil)
		}
	}

	rp := bactype.SubscribeCOVData{
		Lifetime: uint32(lifetime),
		Object: bactype.Object{
			ID: bactype.ObjectID{
				Type:     bactype.ObjectType(ObjectTypeId),
				Instance: bactype.ObjectInstance(ObjectInstance),
			},
			Properties: []bactype.Property{
				{
					Type:       PropertyType,
					ArrayIndex: bacnet.ArrayAll,
				},
			},
		},
	}
	dev := bactype.Device{
		Addr: bactype.Address{
			IPaddr: m.dev.IPAddr,
		},
	}

	var valuebus chan interface{}
	err = m.Client.SubCOV(dev, rp, valuebus)

	kvs := make(map[string]*models.DeviceData, len(ec.Outs))

	for {
		select {
		case <-m.ctx.Done():
			return nil
		case b := <-valuebus:
			r, ok := b.(bactype.SubscribeCOVData)
			// Skip non SubscribeCOVData responses
			if !ok {
				continue
			}
			kvs[propertyID] = &models.DeviceData{
				Name:  property.Name,
				Type:  property.FieldType,
				Value: r.Object.Properties[0].Data,
				Ts:    time.Time{},
			}

			bus <- &models.DeviceDataWrapper{
				DeviceID:   m.dvcID,
				ProductID:  m.pid,
				FuncID:     eventID,
				Properties: kvs,
			}
		}
	}
}

func (m *bacnetTwin) Call(methodID models.ProductMethodID, ins map[models.ProductPropertyID]*models.DeviceData) (outs map[models.ProductPropertyID]*models.DeviceData, err error) {
	//unsupport method
	return nil, nil
}

//获取Propety中ObjectType ，ObjectInstance， PropertyType
func (m *bacnetTwin) getPropertyInfo(propertyID models.ProductPropertyID) (uint16, uint32, uint32, error) {
	property, ok := m.properties[propertyID]
	if !ok {
		return 0, 0, 0, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("the property[%s] hasn't been ready", property.Id), nil)
	}
	//准备ObjectTypeId
	v, ok := property.AuxProps["ObjectTypeId"]
	if !ok {
		return 0, 0, 0, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("Object Type ID hasn't been ready"), nil)
	}
	ObjectTypeId, _ := strconv.Atoi(v)
	//准备ObjectInstance
	v, ok = property.AuxProps["ObjectInstance"]
	if !ok {
		return 0, 0, 0, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("Object Instance hasn't been ready"), nil)
	}
	ObjectInstance, _ := strconv.Atoi(v)
	//准备PropertyType
	v, ok = property.AuxProps["PropertyType"]
	if !ok {
		return 0, 0, 0, errors.NewCommonEdgeError(errors.NotFound, fmt.Sprintf("Property Type hasn't been ready"), nil)
	}
	PropertyType, _ := strconv.Atoi(v)
	return uint16(ObjectTypeId), uint32(ObjectInstance), uint32(PropertyType), nil

}
