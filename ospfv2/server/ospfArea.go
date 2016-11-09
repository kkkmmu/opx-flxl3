//
//Copyright [2016] [SnapRoute Inc]
//
//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
//       Unless required by applicable law or agreed to in writing, software
//       distributed under the License is distributed on an "AS IS" BASIS,
//       WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//       See the License for the specific language governing permissions and
//       limitations under the License.
//
// _______  __       __________   ___      _______.____    __    ____  __  .___________.  ______  __    __
// |   ____||  |     |   ____\  \ /  /     /       |\   \  /  \  /   / |  | |           | /      ||  |  |  |
// |  |__   |  |     |  |__   \  V  /     |   (----` \   \/    \/   /  |  | `---|  |----`|  ,----'|  |__|  |
// |   __|  |  |     |   __|   >   <       \   \      \            /   |  |     |  |     |  |     |   __   |
// |  |     |  `----.|  |____ /  .  \  .----)   |      \    /\    /    |  |     |  |     |  `----.|  |  |  |
// |__|     |_______||_______/__/ \__\ |_______/        \__/  \__/     |__|     |__|      \______||__|  |__|
//

package server

import (
	"errors"
	"l3/ospfv2/objects"
)

type AreaConf struct {
	AdminState       bool
	AuthType         uint8
	ImportASExtern   bool
	NumSpfRuns       uint32
	NumBdrRtr        uint32
	NumAsBdrRtr      uint32
	NumRouterLsa     uint32
	NumNetworkLsa    uint32
	NumSummary3Lsa   uint32
	NumSummary4Lsa   uint32
	NumASExternalLsa uint32
	NumIntfs         uint32
	NumNbrs          uint32
	IntfMap          map[IntfConfKey]bool
}

func genOspfv2AreaUpdateMask(attrset []bool) uint32 {
	var mask uint32 = 0

	if attrset == nil {
		mask = objects.OSPFV2_AREA_UPDATE_AUTH_TYPE |
			objects.OSPFV2_AREA_UPDATE_IMPORT_AS_EXTERN
	} else {
		for idx, val := range attrset {
			if val == true {
				switch idx {
				case 0:
					//AreaId
				case 1:
					mask |= objects.OSPFV2_AREA_UPDATE_ADMIN_STATE
				case 2:
					mask |= objects.OSPFV2_AREA_UPDATE_AUTH_TYPE
				case 3:
					mask |= objects.OSPFV2_AREA_UPDATE_IMPORT_AS_EXTERN
				}
			}
		}
	}
	return mask
}

func (server *OSPFV2Server) updateArea(newCfg, oldCfg *objects.Ospfv2Area, attrset []bool) (bool, error) {
	server.logger.Info("Area configuration update")
	// Stop All the INTF FSM in this area
	nbrKeyList := server.StopAreaIntfFSM(newCfg.AreaId)
	if len(nbrKeyList) > 0 {
		//Delete All the neighbors in this area
		server.SendDeleteNeighborsMsg(nbrKeyList)
	}
	//Send Message to flush router LSA newCfg.AreaId
	server.SendMsgToGenerateRouterLSA(newCfg.AreaId)
	oldAreaEnt, exist := server.AreaConfMap[newCfg.AreaId]
	if !exist {
		server.logger.Err("Cannot update, area doesnot exist")
		return false, errors.New("Cannot update, area doesnot exist")
	}
	newAreaEnt := oldAreaEnt
	mask := genOspfv2AreaUpdateMask(attrset)
	if mask&objects.OSPFV2_AREA_UPDATE_ADMIN_STATE == objects.OSPFV2_AREA_UPDATE_ADMIN_STATE {
		newAreaEnt.AdminState = newCfg.AdminState
	}
	if mask&objects.OSPFV2_AREA_UPDATE_AUTH_TYPE == objects.OSPFV2_AREA_UPDATE_AUTH_TYPE {
		newAreaEnt.AuthType = newCfg.AuthType
	}
	if mask&objects.OSPFV2_AREA_UPDATE_IMPORT_AS_EXTERN == objects.OSPFV2_AREA_UPDATE_IMPORT_AS_EXTERN {
		newAreaEnt.ImportASExtern = newCfg.ImportASExtern
	}

	if newAreaEnt.AdminState == true {
		//Start All the Intf FSM in this area
		server.StartAreaIntfFSM(newCfg.AreaId)
		//Send Message to generate router LSA SendMessage to generate router LSA
		server.SendMsgToGenerateRouterLSA(newCfg.AreaId)
	}
	server.AreaConfMap[newCfg.AreaId] = newAreaEnt
	return true, nil
}

func (server *OSPFV2Server) createArea(cfg *objects.Ospfv2Area) (bool, error) {
	server.logger.Info("Area configuration create")
	areaEnt, exist := server.AreaConfMap[cfg.AreaId]
	if exist {
		server.logger.Err("Unable to Create Area already exist")
		return false, errors.New("Unable to create area already exist")
	}
	//TODO: Only AuthType none is supported
	if cfg.AuthType != objects.AUTH_TYPE_NONE {
		server.logger.Err("Only AuthType None is supported")
		return false, errors.New("AuthType not supported")
	}
	areaEnt.AuthType = cfg.AuthType
	areaEnt.ImportASExtern = cfg.ImportASExtern
	areaEnt.IntfMap = make(map[IntfConfKey]bool)
	server.AreaConfMap[cfg.AreaId] = areaEnt
	if len(server.AreaConfMap) > 1 {
		server.globalData.AreaBdrRtrStatus = true
	} else {
		server.globalData.AreaBdrRtrStatus = false
	}
	return true, nil
}

func (server *OSPFV2Server) deleteArea(cfg *objects.Ospfv2Area) (bool, error) {
	server.logger.Info("Area configuration delete")
	areaEnt, exist := server.AreaConfMap[cfg.AreaId]
	if !exist {
		server.logger.Err("Unable to Delete Area doesnot exist")
		return false, errors.New("Unable to delete area doesnot exist")
	}
	if len(areaEnt.IntfMap) > 0 {
		server.logger.Err("Unable to delete Area as there are interface configured in this area")
		return false, errors.New("Unable to delete Area as there are interface configured in this area")
	}
	delete(server.AreaConfMap, cfg.AreaId)
	if len(server.AreaConfMap) <= 1 {
		server.globalData.AreaBdrRtrStatus = false
	}
	return true, nil
}

func (server *OSPFV2Server) getAreaState(areaId uint32) (*objects.Ospfv2AreaState, error) {
	var retObj objects.Ospfv2AreaState
	server.logger.Info("Area:", server.AreaConfMap[areaId])
	return &retObj, nil
}

func (server *OSPFV2Server) getBulkAreaState(fromIdx, cnt int) (*objects.Ospfv2AreaStateGetInfo, error) {
	var retObj objects.Ospfv2AreaStateGetInfo
	server.logger.Info("Area:", server.AreaConfMap)
	return &retObj, nil
}

func (server *OSPFV2Server) isStubArea(areaId uint32) (bool, error) {
	conf, exist := server.AreaConfMap[areaId]
	if !exist {
		return false, errors.New("Area doesnot exist")
	}

	if conf.ImportASExtern == false {
		return true, nil
	}
	return false, nil
}
