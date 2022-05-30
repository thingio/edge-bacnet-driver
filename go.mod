module github.com/thingio/edge-randnum-driver

replace (
	github.com/thingio/edge-device-driver => ./../edge-device-driver
	github.com/thingio/edge-device-std => ./../edge-device-std
)

require (
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/thingio/edge-device-std v0.2.1
	gopkg.in/check.v1 v1.0.0-20190902080502-41f04d3bba15
)

go 1.16
