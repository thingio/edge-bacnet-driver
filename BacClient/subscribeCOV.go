package gobacnet

import (
	"context"
	"fmt"
	"github.com/alexbeltran/gobacnet/encoding"
	bactype "github.com/alexbeltran/gobacnet/types"
	"time"
)

// ReadProperty reads a single property from a single object in the given device.
func (c *Client) SubCOV(dest bactype.Device, rp bactype.SubscribeCOVData, bus chan interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id, err := c.tsm.ID(ctx)
	if err != nil {
		return fmt.Errorf("unable to get an transaction id: %v", err)
	}
	defer c.tsm.Put(id)
	rp.InvokeID = uint8(id)
	rp.SubscribeProcessId = uint8(id)
	enc := encoding.NewEncoder()
	//encoding NPDU
	enc.NPDU(bactype.NPDU{
		Version:               bactype.ProtocolVersion,
		IsNetworkLayerMessage: false,
		ExpectingReply:        true,
		Priority:              bactype.Normal,
		HopCount:              bactype.DefaultHopCount,
	})
	//encodin APDU
	enc.SubscribeCOV(uint8(id), rp)
	if enc.Error() != nil {
		return err
	}

	// the value filled doesn't matter. it just needs to be non nil
	err = fmt.Errorf("go")
	for count := 0; err != nil && count < 2; count++ {
		_, err = c.send(dest.Addr, enc.Bytes())
		if err != nil {
			continue
		}
		return err
	}

	err = c.utsm.SubscribeCov(int(rp.Object.ID.Instance), int(rp.Object.ID.Instance), bus, rp.Lifetime)
	if err != nil {
		return fmt.Errorf("unable to SubscribeCov for object id :%v", err)
	}
	return err
}
