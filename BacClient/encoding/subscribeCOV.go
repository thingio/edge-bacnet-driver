package encoding

import (
	"fmt"
	bactype "github.com/alexbeltran/gobacnet/types"
)

func (e *Encoder) subscribeCOVHeader(tagPos uint8, data bactype.SubscribeCOVData) (uint8, error) {
	// Validate data first
	if err := isValidObjectType(data.Object.ID.Type); err != nil {
		return 0, err
	}

	// Tag0 - subscribe process identifier
	e.contextSubscribeIdentifier(tagPos, data.SubscribeProcessId)
	tagPos++

	// Tag1 - contextObjectID
	e.contextObjectID(tagPos, data.Object.ID.Type, data.Object.ID.Instance)
	tagPos++

	// Tag2 - issue unconfirmed COV notifications
	e.issueUnconfirmedCOV(tagPos)
	tagPos++

	// Tag3 - subscribe COV lifetime
	if data.Lifetime != 0 {
		e.subscribeCOVlifetime(tagPos, data.Lifetime)
		tagPos++
	}

	return tagPos, nil
}

// SubscribeCOV is a service request to subscribe a property 's COV.
func (e *Encoder) SubscribeCOV(invokeID uint8, data bactype.SubscribeCOVData) error {
	a := bactype.APDU{
		DataType:         bactype.ConfirmedServiceRequest, //confirmed-request-PDU
		Service:          bactype.ServiceConfirmedSubscribeCOV,
		MaxSegs:          0,
		MaxApdu:          MaxAPDU,
		InvokeId:         invokeID,
		SegmentedMessage: false,
	}
	e.APDU(a)
	e.subscribeCOVHeader(initialTagPos, data)
	return e.Error()
}

func (d *Decoder) SubscribeCOV(data *bactype.SubscribeCOVData) error {
	// Must have at least 7 bytes
	if d.buff.Len() < 7 {
		return fmt.Errorf("Missing parameters")
	}

	// Tag 0:   SubscribeProcessId
	tag, meta := d.tagNumber()
	var expectedTag uint8
	if tag != expectedTag {
		return &ErrorIncorrectTag{expectedTag, tag}
	}
	expectedTag++
	if !meta.isContextSpecific() {
		return fmt.Errorf("Tag %d should be context specific. %x", tag, meta)
	}
	//读取SubscribeProcessId
	data.SubscribeProcessId = d.subscribeProcessId()

	// Tag 1: Device ID
	tag, meta = d.tagNumber()
	if tag != expectedTag {
		return &ErrorIncorrectTag{expectedTag, tag}
	}
	expectedTag++
	d.objectId()
	if !meta.isContextSpecific() {
		return fmt.Errorf("Tag %d should be context specific. %x", tag, meta)
	}

	// Tag 2: Object ID
	tag, meta = d.tagNumber()
	if tag != expectedTag {
		return &ErrorIncorrectTag{expectedTag, tag}
	}
	expectedTag++
	//读取objectID 与 type
	objectType, instance := d.objectId()
	data.Object.ID.Type = objectType
	data.Object.ID.Instance = instance

	// Tag 3: Subscribe time remaining
	tag, meta, _ = d.tagNumberAndValue()
	if tag != expectedTag {
		return &ErrorIncorrectTag{expectedTag, tag}
	}
	expectedTag++

	// Tag 4 : opening tag
	tag, meta = d.tagNumber()
	if tag != expectedTag {
		return &ErrorIncorrectTag{Expected: expectedTag, Given: tag}
	}
	if !meta.isOpening() {
		return &ErrorWrongTagType{OpeningTag}
	}

	// New Tag 0 : Property ID
	tag, meta = d.tagNumber()
	lenValue := d.value(meta) //取property ID tag的值，即为propertyID的字节长度
	var prop bactype.Property
	prop.Type = d.enumerated(int(lenValue))

	// reading data
	tag, meta = d.tagNumber()
	value, err := d.AppData()
	if err != nil {
		d.err = err
		return err
	}
	prop.Data = value
	data.Object.Properties = []bactype.Property{prop}

	return d.Error()
}
