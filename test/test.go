package main

import (
	"fmt"
	"github.com/thingio/edge-randnum-driver/bacnet"
	"net"
)

func main() {
	addr, _ := net.ResolveUDPAddr("udp", fmt.Sprintf("192.168.31.50:47808"))
	conn, err := net.Dial("udp", addr.String())
	if err != nil {
		fmt.Println(err)
	}
	log := &bacnet.Log{}
	data, err, _ := bacnet.SendReadProperty(log, conn, bacnet.ObjectID(1), bacnet.PropertyID(85))
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(string(data))
}
