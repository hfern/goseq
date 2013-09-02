package goseq

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// Region is a part of the world in which
// Source servers are segregated. They should
// have made these flags so you query multiple
// regions at once. But alas, they did not.
// Let's hope it ends up in Source II.
type Region byte

const (
	USEast       Region = 0x00
	USWest              = 0x01
	SouthAmerica        = 0x02
	Europe              = 0x03
	Asia                = 0x04
	Australia           = 0x05
	MiddleEast          = 0x06
	Africa              = 0x07
	RestOfWorld         = 0xFF
)

const (
	// Beggining the the special ip address that Master
	// interprets as the beggining and end of an IP listing.
	Beggining string = "0.0.0.0:0"
)

const (
	masterRespHeaderLength int = 6
)

var (
	// MasterSourceServers are the ip addresses of the Master servers for all Source engine servers.
	MasterSourceServers []string = []string{
		"68.177.101.62:27011",
		"69.28.158.131:27011",
		"208.64.200.117:27011",
		"208.64.200.118:27011",
	}
	// which server we're going to use by default
	favored_server int = 2

	MasterServerTimeout time.Duration = 5 * time.Second

	err_timeout error = errors.New("Couldn't read from master server.")
)

var (
	masterResponseHeader [masterRespHeaderLength]byte = [...]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x66, 0x0A}
)

type MasterServer interface {
	SetFilter(Filter) error
	GetFilter() Filter
	SetAddr(string) error
	GetAddr() string
	SetRegion(Region)
	GetRegion() Region
	// Query returns results starting at the server
	// with startIP.
	// Returned servers are NOT guaranteed to work.
	Query(startIP string) ([]Server, error)
}

type master struct {
	filter       Filter
	addr         string
	master_index int
	region       Region
	remoteAddr   *net.UDPAddr
	remoteConn   *net.UDPConn
}

func NewMasterServer() MasterServer {
	return &master{
		filter:       NewFilter(),
		addr:         MasterSourceServers[favored_server],
		master_index: favored_server,
		region:       USWest,
		remoteAddr:   nil,
		remoteConn:   nil,
	}
}

func (m *master) SetFilter(f Filter) error { m.filter = f; return nil }
func (m *master) GetFilter() Filter        { return m.filter }
func (m *master) SetAddr(i string) error   { m.addr = i; m.remoteAddr = nil; return nil }
func (m *master) GetAddr() string          { return m.addr }
func (m *master) SetRegion(i Region)       { m.region = i }
func (m *master) GetRegion() Region        { return m.region }

func (m *master) refreshConnection() (err error) {
	if m.remoteAddr == nil || m.remoteConn == nil {
		m.remoteAddr, err = net.ResolveUDPAddr("udp", m.addr)
		if err != nil {
			m.remoteAddr = nil
			return err
		}
		m.remoteConn, err = net.DialUDP("udp", nil, m.remoteAddr)
		if err != nil {
			m.remoteAddr = nil
			return err
		}
	}
	return nil
}

func (m *master) makerequest(ip string) []byte {
	packet := bytes.NewBuffer([]byte{})
	packet.WriteByte(0x31)
	packet.WriteByte(byte(m.region))
	packet.WriteString(ip)
	packet.WriteByte(0x0)
	packet.WriteString(string(m.filter.GetFilterFormat()))

	req := packet.Bytes()
	return req
}

// performs no allocations to keep it fast
// iterating over hundreds of servers.
func (_ *master) ip2server(ip wireIP, serv *iserver) {
	serv.addr = ip.String()
}

func (m *master) try(request, buffer []byte) (error, int) {
	timeout := make(chan bool, 1)
	done := make(chan error, 1)
	n := 0
	var e error

	go func() {
		if e := m.refreshConnection(); e != nil {
			done <- e
			return
		}

		if _, e := m.remoteConn.Write(request); e != nil {
			done <- e
			return
		}

		n, e = m.remoteConn.Read(buffer)
		if e != nil {
			done <- e
			return
		}
		done <- nil
	}()

	go func() {
		time.Sleep(MasterServerTimeout)
		timeout <- true
	}()

	select {
	case e := <-done:
		return e, n
	case <-timeout:
		return err_timeout, 0
	}
}

func (m *master) Query(at string) ([]Server, error) {
	reqpacket := m.makerequest(at)
	respbuffer := [1024 * 1024 * 2]byte{} // 2MB, should be 400 bytes above max

	var e error
	var n int

	start_indice := m.master_index
	for {
		e, n = m.try(reqpacket, respbuffer[0:])
		if e == err_timeout {
			m.master_index = (m.master_index + 1) % len(MasterSourceServers)
			m.addr = MasterSourceServers[m.master_index]

			if m.master_index == start_indice {
				// we've come full circle, time to quit.
				return nil, err_timeout
			}

			favored_server = m.master_index
			m.remoteAddr = nil
		} else if e != nil {
			return nil, e
		} else {
			break
		}
	}

	resp := wireMasterResponse{}
	err := resp.Decode(bytes.NewBuffer(respbuffer[0:n]))
	if err != nil {
		return nil, err
	}

	servers := make([]Server, len(resp.ips))

	for i, ip := range resp.ips {
		servers[i] = iserver{addr: ip.String()}
	}

	return servers, nil
}

// Incoming IPs as represented on the wire.
type wireIP struct {
	oct struct {
		o1,
		o2,
		o3,
		o4 byte
	}
	// ATCHTUNG!!! This is NETWORK BYTE ORDERED
	// as defined by the spec.
	port uint16
}

func (p wireIP) String() string {
	return fmt.Sprintf("%d.%d.%d.%d:%d",
		p.oct.o1, p.oct.o2, p.oct.o3, p.oct.o4, p.port)
}

type wireMasterResponse struct {
	head struct {
		magic [masterRespHeaderLength]byte
	}
	ips []wireIP
}

func (r *wireMasterResponse) Decode(packet io.Reader) error {
	err := binary.Read(packet, byteOrder, &r.head)
	if err != nil {
		return err
	}

	return nil
}
