// server.go
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"l3/bgp/config"
	"l3/bgp/packet"
	"l3/rib/ribdCommonDefs"
	"log/syslog"
	"net"
	"ribd"
	"runtime"
	"sync"
	"sync/atomic"

	nanomsg "github.com/op/go-nanomsg"
)

const IP string = "12.1.12.202" //"192.168.1.1"
const BGPPort string = "179"

type PeerUpdate struct {
	OldPeer config.NeighborConfig
	NewPeer config.NeighborConfig
	AttrSet []bool
}

type PeerGroupUpdate struct {
	OldGroup config.PeerGroupConfig
	NewGroup config.PeerGroupConfig
	AttrSet  []bool
}

type BGPServer struct {
	logger           *syslog.Writer
	ribdClient       *ribd.RouteServiceClient
	BgpConfig        config.Bgp
	GlobalConfigCh   chan config.GlobalConfig
	AddPeerCh        chan PeerUpdate
	RemPeerCh        chan string
	AddPeerGroupCh   chan PeerGroupUpdate
	RemPeerGroupCh   chan string
	PeerConnEstCh    chan string
	PeerConnBrokenCh chan string
	PeerCommandCh    chan config.PeerCommand
	BGPPktSrc        chan *packet.BGPPktSrc

	NeighborMutex  sync.RWMutex
	PeerMap        map[string]*Peer
	Neighbors      []*Peer
	AdjRib         *AdjRib
	connRoutesPath *Path
}

func NewBGPServer(logger *syslog.Writer, ribdClient *ribd.RouteServiceClient) *BGPServer {
	bgpServer := &BGPServer{}
	bgpServer.logger = logger
	bgpServer.ribdClient = ribdClient
	bgpServer.GlobalConfigCh = make(chan config.GlobalConfig)
	bgpServer.AddPeerCh = make(chan PeerUpdate)
	bgpServer.RemPeerCh = make(chan string)
	bgpServer.AddPeerGroupCh = make(chan PeerGroupUpdate)
	bgpServer.RemPeerGroupCh = make(chan string)
	bgpServer.PeerConnEstCh = make(chan string)
	bgpServer.PeerConnBrokenCh = make(chan string)
	bgpServer.PeerCommandCh = make(chan config.PeerCommand)
	bgpServer.BGPPktSrc = make(chan *packet.BGPPktSrc)
	bgpServer.NeighborMutex = sync.RWMutex{}
	bgpServer.PeerMap = make(map[string]*Peer)
	bgpServer.Neighbors = make([]*Peer, 0)
	bgpServer.AdjRib = NewAdjRib(bgpServer)
	return bgpServer
}

func (server *BGPServer) listenForPeers(acceptCh chan *net.TCPConn) {
	addr := ":" + BGPPort
	server.logger.Info(fmt.Sprintf("Listening for incomig connections on %s\n", addr))
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		server.logger.Info(fmt.Sprintln("ResolveTCPAddr failed with", err))
	}

	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		server.logger.Info(fmt.Sprintln("ListenTCP failed with", err))
	}

	for {
		server.logger.Info(fmt.Sprintln("Waiting for peer connections..."))
		tcpConn, err := listener.AcceptTCP()
		if err != nil {
			server.logger.Info(fmt.Sprintln("AcceptTCP failed with", err))
			continue
		}
		server.logger.Info(fmt.Sprintln("Got a peer connection from %s", tcpConn.RemoteAddr()))
		acceptCh <- tcpConn
	}
}

func (server *BGPServer) setupRibSubSocket(address string) (*nanomsg.SubSocket, error) {
	var err error
	var socket *nanomsg.SubSocket
	if socket, err = nanomsg.NewSubSocket(); err != nil {
		server.logger.Err(fmt.Sprintln("Failed to create RIB subscribe socket, error:", err))
		return nil, err
	}

	if err = socket.Subscribe(""); err != nil {
		server.logger.Err(fmt.Sprintln("Failed to subscribe to \"\" on RIB subscribe socket, error:", err))
		return nil, err
	}

	if _, err = socket.Connect(address); err != nil {
		server.logger.Err(fmt.Sprintln("Failed to connect to RIB publisher socket, address:", address, "error:", err))
		return nil, err
	}

	server.logger.Info(fmt.Sprintln("Connected to RIB publisher at address:", address))
	if err = socket.SetRecvBuffer(1024 * 1024); err != nil {
		server.logger.Err(fmt.Sprintln("Failed to set the buffer size for RIB publisher socket, error:", err))
		return nil, err
	}
	return socket, nil
}

func (server *BGPServer) listenForRIBUpdates(socket *nanomsg.SubSocket, socketCh chan []byte, socketErrCh chan error) {
	for {
		server.logger.Info("Read on RIB subscriber socket...")
		rxBuf, err := socket.Recv(0)
		if err != nil {
			server.logger.Err(fmt.Sprintln("Recv on RIB subscriber socket failed with error:", err))
			socketErrCh <- err
			continue
		}
		server.logger.Info(fmt.Sprintln("RIB subscriber recv returned:", rxBuf))
		socketCh <- rxBuf
	}
}

func (server *BGPServer) handleRibUpdates(rxBuf []byte) {
	var route ribdCommonDefs.RoutelistInfo
	routes := make([]*ribd.Routes, 0)
	reader := bytes.NewReader(rxBuf)
	decoder := json.NewDecoder(reader)
	msg := ribdCommonDefs.RibdNotifyMsg{}
	for err := decoder.Decode(&msg); err == nil; err = decoder.Decode(&msg) {
		err = json.Unmarshal(msg.MsgBuf, &route)
		if err != nil {
			server.logger.Err("Err in processing routes from RIB")
		}
		server.logger.Info(fmt.Sprintln("Remove connected route, dest:", route.RouteInfo.Ipaddr, "netmask:", route.RouteInfo.Mask, "nexthop:", route.RouteInfo.NextHopIp))
		routes = append(routes, &route.RouteInfo)
	}

	if len(routes) > 0 {
		if msg.MsgType == ribdCommonDefs.NOTIFY_ROUTE_CREATED {
			server.ProcessConnectedRoutes(routes, make([]*ribd.Routes, 0))
		} else if msg.MsgType == ribdCommonDefs.NOTIFY_ROUTE_DELETED {
			server.ProcessConnectedRoutes(make([]*ribd.Routes, 0), routes)
		} else {
			server.logger.Err(fmt.Sprintf("**** Received RIB update with unknown type %d ****", msg.MsgType))
		}
	} else {
		server.logger.Err(fmt.Sprintf("**** Received RIB update type %d with no routes ****", msg.MsgType))
	}
}

func (server *BGPServer) IsPeerLocal(peerIp string) bool {
	return server.PeerMap[peerIp].PeerConf.PeerAS == server.BgpConfig.Global.Config.AS
}

func (server *BGPServer) sendUpdateMsgToAllPeers(msg *packet.BGPMessage, path *Path) {
	for _, peer := range server.PeerMap {
		// If we recieve the route from IBGP peer, don't send it to other IBGP peers
		if path.peer != nil {
			if path.peer.IsInternal() {

				if peer.IsInternal() && !path.peer.IsRouteReflectorClient() && !peer.IsRouteReflectorClient() {
					continue
				}
			}

			// Don't send the update to the peer that sent the update.
			if peer.PeerConf.NeighborAddress.String() == path.peer.PeerConf.NeighborAddress.String() {
				continue
			}
		}

		peer.SendUpdate(*msg.Clone(), path)
	}
}

func (server *BGPServer) SendUpdate(updated map[*Path][]packet.IPPrefix, withdrawn []packet.IPPrefix, withdrawPath *Path) {
	if len(withdrawn) > 0 {
		updateMsg := packet.NewBGPUpdateMessage(withdrawn, nil, nil)
		server.sendUpdateMsgToAllPeers(updateMsg, withdrawPath)
	}

	for path, dest := range updated {
		updateMsg := packet.NewBGPUpdateMessage(make([]packet.IPPrefix, 0), path.pathAttrs, dest)
		server.sendUpdateMsgToAllPeers(updateMsg, path)
	}
}

func (server *BGPServer) ProcessUpdate(pktInfo *packet.BGPPktSrc) {
	peer, ok := server.PeerMap[pktInfo.Src]
	if !ok {
		server.logger.Err(fmt.Sprintln("BgpServer:ProcessUpdate - Peer not found, address:", pktInfo.Src))
		return
	}

	atomic.AddUint32(&peer.Neighbor.State.Queues.Input, ^uint32(0))
	peer.Neighbor.State.Messages.Received.Update++
	updated, withdrawn, withdrawPath := server.AdjRib.ProcessUpdate(peer, pktInfo)
	server.SendUpdate(updated, withdrawn, withdrawPath)
}

func (server *BGPServer) convertDestIPToIPPrefix(routes []*ribd.Routes) []packet.IPPrefix {
	dest := make([]packet.IPPrefix, 0, len(routes))
	for _, r := range routes {
		ipPrefix := packet.ConstructIPPrefix(r.Ipaddr, r.Mask)
		dest = append(dest, *ipPrefix)
	}
	return dest
}

func (server *BGPServer) ProcessConnectedRoutes(installedRoutes []*ribd.Routes, withdrawnRoutes []*ribd.Routes) {
	server.logger.Info(fmt.Sprintln("valid routes:", installedRoutes, "invalid routes:", withdrawnRoutes))
	valid := server.convertDestIPToIPPrefix(installedRoutes)
	invalid := server.convertDestIPToIPPrefix(withdrawnRoutes)
	updated, withdrawn, withdrawPath := server.AdjRib.ProcessConnectedRoutes(server.BgpConfig.Global.Config.RouterId.String(),
		server.connRoutesPath, valid, invalid)
	server.SendUpdate(updated, withdrawn, withdrawPath)
}

func (server *BGPServer) ProcessRemoveNeighbor(peerIp string, peer *Peer) {
	updated, withdrawn, withdrawPath := server.AdjRib.RemoveUpdatesFromNeighbor(peerIp, peer)
	server.SendUpdate(updated, withdrawn, withdrawPath)
}

func (server *BGPServer) SendAllRoutesToPeer(peer *Peer) {
	withdrawn := make([]packet.IPPrefix, 0)
	updated := server.AdjRib.GetLocRib()
	for path, dest := range updated {
		updateMsg := packet.NewBGPUpdateMessage(withdrawn, path.pathAttrs, dest)
		peer.SendUpdate(*updateMsg.Clone(), path)
	}
}

func (server *BGPServer) RemoveRoutesFromAllNeighbor() {
	server.AdjRib.RemoveUpdatesFromAllNeighbors()
}

func (server *BGPServer) addPeerToList(peer *Peer) {
	server.Neighbors = append(server.Neighbors, peer)
}

func (server *BGPServer) removePeerFromList(peer *Peer) {
	for idx, item := range server.Neighbors {
		if item == peer {
			server.Neighbors[idx] = server.Neighbors[len(server.Neighbors)-1]
			server.Neighbors[len(server.Neighbors)-1] = nil
			server.Neighbors = server.Neighbors[:len(server.Neighbors)-1]
			break
		}
	}
}

func (server *BGPServer) StopPeersByGroup(groupName string) []*Peer {
	peers := make([]*Peer, 0)
	for peerIP, peer := range server.PeerMap {
		if peer.PeerGroup.Name == groupName {
			server.logger.Info(fmt.Sprintln("Clean up peer", peerIP))
			peer.Cleanup()
			server.ProcessRemoveNeighbor(peerIP, peer)
			peers = append(peers, peer)

			runtime.Gosched()
		}
	}

	return peers
}

func (server *BGPServer) UpdatePeerGroupInPeers(groupName string, peerGroup *config.PeerGroupConfig) {
	peers := server.StopPeersByGroup(groupName)
	for _, peer := range peers {
		peer.UpdatePeerGroup(peerGroup)
		peer.Init()
	}
}

func (server *BGPServer) copyGlobalConf(gConf config.GlobalConfig) {
	server.BgpConfig.Global.Config.AS = gConf.AS
	server.BgpConfig.Global.Config.RouterId = gConf.RouterId
}

func (server *BGPServer) StartServer() {
	gConf := <-server.GlobalConfigCh
	server.logger.Info(fmt.Sprintln("Recieved global conf:", gConf))
	server.BgpConfig.Global.Config = gConf
	server.BgpConfig.PeerGroups = make(map[string]*config.PeerGroup)

	pathAttrs := packet.ConstructPathAttrForConnRoutes(gConf.RouterId, gConf.AS)
	server.connRoutesPath = NewPath(server, nil, pathAttrs, false, false, RouteTypeConnected)

	server.logger.Info("Listen for RIBd updates")
	ribSubSocket, _ := server.setupRibSubSocket(ribdCommonDefs.PUB_SOCKET_ADDR)
	ribSubBGPSocket, _ := server.setupRibSubSocket(ribdCommonDefs.PUB_SOCKET_BGPD_ADDR)

	ribSubSocketCh := make(chan []byte)
	ribSubSocketErrCh := make(chan error)
	ribSubBGPSocketCh := make(chan []byte)
	ribSubBGPSocketErrCh := make(chan error)

	server.logger.Info("Setting up Peer connections")
	acceptCh := make(chan *net.TCPConn)
	go server.listenForPeers(acceptCh)

	routes, _ := server.ribdClient.GetConnectedRoutesInfo()
	server.ProcessConnectedRoutes(routes, make([]*ribd.Routes, 0))

	go server.listenForRIBUpdates(ribSubSocket, ribSubSocketCh, ribSubSocketErrCh)
	go server.listenForRIBUpdates(ribSubBGPSocket, ribSubBGPSocketCh, ribSubBGPSocketErrCh)

	for {
		select {
		case gConf = <-server.GlobalConfigCh:
			for peerIP, peer := range server.PeerMap {
				server.logger.Info(fmt.Sprintf("Cleanup peer %s", peerIP))
				peer.Cleanup()
			}
			server.logger.Info(fmt.Sprintf("Giving up CPU so that all peer FSMs will get cleaned up"))
			runtime.Gosched()

			packet.SetNextHopPathAttrs(server.connRoutesPath.pathAttrs, gConf.RouterId)
			server.RemoveRoutesFromAllNeighbor()
			server.copyGlobalConf(gConf)
			for _, peer := range server.PeerMap {
				peer.Init()
			}

		case peerUpdate := <-server.AddPeerCh:
			oldPeer := peerUpdate.OldPeer
			newPeer := peerUpdate.NewPeer
			var peer *Peer
			var ok bool
			if oldPeer.NeighborAddress != nil {
				if peer, ok = server.PeerMap[oldPeer.NeighborAddress.String()]; ok {
					server.logger.Info(fmt.Sprintln("Clean up peer", oldPeer.NeighborAddress.String()))
					peer.Cleanup()
					server.ProcessRemoveNeighbor(oldPeer.NeighborAddress.String(), peer)
					peer.UpdateNeighborConf(newPeer)

					runtime.Gosched()
				} else {
					server.logger.Info(fmt.Sprintln("Can't find neighbor with old address",
						oldPeer.NeighborAddress.String()))
				}
			}

			if !ok {
				_, ok = server.PeerMap[newPeer.NeighborAddress.String()]
				if ok {
					server.logger.Info(fmt.Sprintln("Failed to add neighbor. Neighbor at that address already exists,",
						newPeer.NeighborAddress.String()))
					break
				}

				var groupConfig *config.PeerGroupConfig
				if newPeer.PeerGroup != "" {
					if group, ok := server.BgpConfig.PeerGroups[newPeer.PeerGroup]; !ok {
						server.logger.Info(fmt.Sprintln("Peer group", newPeer.PeerGroup, "not created yet, creating peer",
							newPeer.NeighborAddress.String(), "without the group"))
					} else {
						groupConfig = &group.Config
					}
				}
				server.logger.Info(fmt.Sprintln("Add neighbor, ip:", newPeer.NeighborAddress.String()))
				peer = NewPeer(server, &server.BgpConfig.Global.Config, groupConfig, newPeer)
				server.PeerMap[newPeer.NeighborAddress.String()] = peer
				server.NeighborMutex.Lock()
				server.addPeerToList(peer)
				server.NeighborMutex.Unlock()
			}
			peer.Init()

		case remPeer := <-server.RemPeerCh:
			server.logger.Info(fmt.Sprintln("Remove Peer:", remPeer))
			peer, ok := server.PeerMap[remPeer]
			if !ok {
				server.logger.Info(fmt.Sprintln("Failed to remove peer. Peer at that address does not exist,", remPeer))
				break
			}
			server.NeighborMutex.Lock()
			server.removePeerFromList(peer)
			server.NeighborMutex.Unlock()
			delete(server.PeerMap, remPeer)
			peer.Cleanup()
			server.ProcessRemoveNeighbor(remPeer, peer)

		case groupUpdate := <-server.AddPeerGroupCh:
			oldGroupConf := groupUpdate.OldGroup
			newGroupConf := groupUpdate.NewGroup
			server.logger.Info(fmt.Sprintln("Peer group update old:", oldGroupConf, "new:", newGroupConf))
			var ok bool

			if oldGroupConf.Name != "" {
				if _, ok = server.BgpConfig.PeerGroups[oldGroupConf.Name]; !ok {
					server.logger.Err(fmt.Sprintln("Could not find peer group", oldGroupConf.Name))
					break
				}
			}

			if _, ok = server.BgpConfig.PeerGroups[newGroupConf.Name]; !ok {
				server.logger.Info(fmt.Sprintln("Add new peer group with name", newGroupConf.Name))
				peerGroup := config.PeerGroup{
					Config: newGroupConf,
				}
				server.BgpConfig.PeerGroups[newGroupConf.Name] = &peerGroup
			}
			server.UpdatePeerGroupInPeers(newGroupConf.Name, &newGroupConf)

		case groupName := <-server.RemPeerGroupCh:
			server.logger.Info(fmt.Sprintln("Remove Peer group:", groupName))
			if _, ok := server.BgpConfig.PeerGroups[groupName]; !ok {
				server.logger.Info(fmt.Sprintln("Peer group", groupName, "not found"))
				break
			}
			delete(server.BgpConfig.PeerGroups, groupName)
			server.UpdatePeerGroupInPeers(groupName, nil)

		case tcpConn := <-acceptCh:
			server.logger.Info(fmt.Sprintln("Connected to", tcpConn.RemoteAddr().String()))
			host, _, _ := net.SplitHostPort(tcpConn.RemoteAddr().String())
			peer, ok := server.PeerMap[host]
			if !ok {
				server.logger.Info(fmt.Sprintln("Can't accept connection. Peer is not configured yet", host))
				tcpConn.Close()
				server.logger.Info(fmt.Sprintln("Closed connection from", host))
				break
			}
			peer.AcceptConn(tcpConn)

		case peerCommand := <-server.PeerCommandCh:
			server.logger.Info(fmt.Sprintln("Peer Command received", peerCommand))
			peer, ok := server.PeerMap[peerCommand.IP.String()]
			if !ok {
				server.logger.Info(fmt.Sprintf("Failed to apply command %s. Peer at that address does not exist, %v\n",
					peerCommand.Command, peerCommand.IP))
			}
			peer.Command(peerCommand.Command)

		case peerIP := <-server.PeerConnEstCh:
			server.logger.Info(fmt.Sprintf("Server: Peer %s FSM connection established", peerIP))
			peer, ok := server.PeerMap[peerIP]
			if !ok {
				server.logger.Info(fmt.Sprintf("Failed to process FSM connection success, Peer %s does not exist", peerIP))
				break
			}
			server.SendAllRoutesToPeer(peer)

		case peerIP := <-server.PeerConnBrokenCh:
			server.logger.Info(fmt.Sprintf("Server: Peer %s FSM connection broken", peerIP))
			peer, ok := server.PeerMap[peerIP]
			if !ok {
				server.logger.Info(fmt.Sprintf("Failed to process FSM connection failure, Peer %s does not exist", peerIP))
				break
			}
			server.ProcessRemoveNeighbor(peerIP, peer)

		case pktInfo := <-server.BGPPktSrc:
			server.logger.Info(fmt.Sprintln("Received BGP message from peer %s", pktInfo.Src))
			server.ProcessUpdate(pktInfo)

		case rxBuf := <-ribSubSocketCh:
			server.logger.Info(fmt.Sprintf("Server: Received update on RIB sub socket"))
			server.handleRibUpdates(rxBuf)

		case err := <-ribSubSocketErrCh:
			server.logger.Info(fmt.Sprintf("Server: RIB subscriber socket returned err:%s", err))

		case rxBuf := <-ribSubBGPSocketCh:
			server.logger.Info(fmt.Sprintf("Server: Received update on RIB BGP sub socket"))
			server.handleRibUpdates(rxBuf)

		case err := <-ribSubBGPSocketErrCh:
			server.logger.Info(fmt.Sprintf("Server: RIB BGP subscriber socket returned err:%s", err))
		}
	}
}

func (s *BGPServer) GetBGPGlobalState() config.GlobalState {
	return s.BgpConfig.Global.State
}

func (s *BGPServer) GetBGPNeighborState(neighborIP string) *config.NeighborState {
	peer, ok := s.PeerMap[neighborIP]
	if !ok {
		s.logger.Err(fmt.Sprintf("GetBGPNeighborState - Neighbor not found for address:%s", neighborIP))
		return nil
	}
	return &peer.Neighbor.State
}

func (s *BGPServer) BulkGetBGPNeighbors(index int, count int) (int, int, []*config.NeighborState) {
	defer s.NeighborMutex.RUnlock()

	s.NeighborMutex.RLock()
	if index+count > len(s.Neighbors) {
		count = len(s.Neighbors) - index
	}

	result := make([]*config.NeighborState, count)
	for i := 0; i < count; i++ {
		result[i] = &s.Neighbors[i+index].Neighbor.State
	}

	index += count
	if index >= len(s.Neighbors) {
		index = 0
	}
	return index, count, result
}
