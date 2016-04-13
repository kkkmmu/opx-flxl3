package server

import (
	"github.com/google/gopacket/pcap"
	"net"
	"sync"
	"time"
)

var ALLSPFROUTER string = "224.0.0.5"
var ALLDROUTER string = "224.0.0.6"
var ALLSPFROUTERMAC string = "01:00:5e:00:00:05"
var ALLDROUTERMAC string = "01:00:5e:00:00:06"
var MASKMAC string = "ff:ff:ff:ff:ff:ff"

var LSInfinity uint32 = 0x00ffffff

type OspfHdrMetadata struct {
	pktType  OspfType
	pktlen   uint16
	backbone bool
	routerId []byte
	areaId   uint32
}

func NewOspfHdrMetadata() *OspfHdrMetadata {
	return &OspfHdrMetadata{}
}

type DstIPType uint8

const (
	Normal       DstIPType = 1
	AllSPFRouter DstIPType = 2
	AllDRouter   DstIPType = 3
)

type IpHdrMetadata struct {
	srcIP     []byte
	dstIP     []byte
	dstIPType DstIPType
}

func NewIpHdrMetadata() *IpHdrMetadata {
	return &IpHdrMetadata{}
}

type EthHdrMetadata struct {
	srcMAC net.HardwareAddr
}

func NewEthHdrMetadata() *EthHdrMetadata {
	return &EthHdrMetadata{}
}

var (
	snapshot_len int32         = 65549 //packet capture length
	promiscuous  bool          = false //mode
	timeout_pcap time.Duration = 5 * time.Second
)

const (
	OSPF_HELLO_MIN_SIZE  = 20
	OSPF_DBD_MIN_SIZE    = 8
	OSPF_LSA_HEADER_SIZE = 20
	OSPF_LSA_REQ_SIZE    = 12
	OSPF_LSA_ACK_SIZE    = 20
	OSPF_HEADER_SIZE     = 24
	IP_HEADER_MIN_LEN    = 20
	OSPF_PROTO_ID        = 89
	OSPF_VERSION_2       = 2
	OSPF_NO_OF_LSA_FIELD = 4
)

type OspfType uint8

const (
	HelloType         OspfType = 1
	DBDescriptionType OspfType = 2
	LSRequestType     OspfType = 3
	LSUpdateType      OspfType = 4
	LSAckType         OspfType = 5
)

type IntfToNeighMsg struct {
	IntfConfKey  IntfConfKey
	RouterId     uint32
	RtrPrio      uint8
	NeighborIP   net.IP
	nbrDeadTimer time.Duration
	TwoWayStatus bool
	nbrDR        []byte
	nbrBDR       []byte
	nbrMAC       net.HardwareAddr
}

type NbrStateChangeMsg struct {
	RouterId uint32
}

const (
	EOption  = 0x02
	MCOption = 0x04
	NPOption = 0x08
	EAOption = 0x20
	DCOption = 0x40
)

type IntfTxHandle struct {
	SendPcapHdl *pcap.Handle
	SendMutex   *sync.Mutex
}

type IntfRxHandle struct {
	RecvPcapHdl     *pcap.Handle
	PktRecvCh       chan bool
	PktRecvStatusCh chan bool
	//RecvMutex               *sync.Mutex
}

type AdjOKEvtMsg struct {
	NewDRtrId  uint32
	OldDRtrId  uint32
	NewBDRtrId uint32
	OldBDRtrId uint32
}

type NbrFullStateMsg struct {
	FullState bool
	NbrRtrId  uint32
}

const (
	LsdbEntryFound    = 0
	LsdbEntryNotFound = 1
)
