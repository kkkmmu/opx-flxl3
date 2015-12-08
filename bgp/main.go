// main.go
package main

import (
	"flag"
	"fmt"
	"l3/bgp/rpc"
	"l3/bgp/server"
	"log/syslog"
	"ribd"
)

const IP string = "localhost" //"10.0.2.15"
const BGPPort string = "179"
const CONF_PORT string = "2001"
const BGPConfPort string = "4050"
const RIBConfPort string = "5000"

func main() {
	fmt.Println("Start the logger")
	logger, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "SR BGP")
	if err != nil {
		fmt.Println("Failed to start the logger. Exit!")
		return
	}
	logger.Info("Started the logger successfully.")

	paramsDir := flag.String("params", "./params", "Params directory")
	flag.Parse()
	fileName := *paramsDir
	if fileName[len(fileName)-1] != '/' {
		fileName = fileName + "/"
	}
	fileName = fileName + "clients.json"

	var ribdClient *ribd.RouteServiceClient = nil
	ribdClientChan := make(chan *ribd.RouteServiceClient)

	go rpc.StartClient(logger, fileName, ribdClientChan)

	ribdClient = <-ribdClientChan
	logger.Info("Connected to RIBd")
	if ribdClient == nil {
		logger.Err("Failed to connect to RIBd\n")
		return
	}

	logger.Info(fmt.Sprintln("Starting BGP Server..."))
	bgpServer := server.NewBGPServer(logger, ribdClient)
	go bgpServer.StartServer()

	logger.Info(fmt.Sprintln("Starting config listener..."))
	confIface := rpc.NewBGPHandler(bgpServer, logger)
	rpc.StartServer(logger, confIface, fileName)
}
