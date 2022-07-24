package main

import (
	"fmt"
	bacnet "github.com/alexbeltran/gobacnet"
	bactype "github.com/alexbeltran/gobacnet/types"
)

func main() {
	c := bacnet.Client{}
	err := c.NewClient1()
	if err != nil {
		return
	}
	a := 0
	b := uint16(a)

	rp := bactype.ReadPropertyData{
		Object: bactype.Object{
			ID: bactype.ObjectID{
				Type:     bactype.ObjectType(b),
				Instance: 2,
			},
			Properties: []bactype.Property{
				bactype.Property{
					Type:       85, // Present value
					ArrayIndex: bacnet.ArrayAll,
					Data:       uint32(1),
				},
			},
		},
	}
	dev := bactype.Device{
		Addr: bactype.Address{
			IPaddr: "192.168.1.2:47808",
		},
	}

	var out bactype.ReadPropertyData
	out, err = c.ReadProperty(dev, rp)
	//Client.SubCOV(dev, rp , valuebus)
	if err != nil {
		return
	}
	fmt.Println(out.Object.Properties[0].Data)
}
