//
//Copyright [2016] [SnapRoute Inc]
//
//Licensed under the Apache License, Version 2.0 (the "License");
//you may not use this file except in compliance with the License.
//You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
//	 Unless required by applicable law or agreed to in writing, software
//	 distributed under the License is distributed on an "AS IS" BASIS,
//	 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	 See the License for the specific language governing permissions and
//	 limitations under the License.
//
// _______  __       __________   ___      _______.____    __    ____  __  .___________.  ______  __    __
// |   ____||  |     |   ____\  \ /  /     /       |\   \  /  \  /   / |  | |           | /      ||  |  |  |
// |  |__   |  |     |  |__   \  V  /     |   (----` \   \/    \/   /  |  | `---|  |----`|  ,----'|  |__|  |
// |   __|  |  |     |   __|   >   <       \   \      \            /   |  |     |  |     |  |     |   __   |
// |  |     |  `----.|  |____ /  .  \  .----)   |      \    /\    /    |  |     |  |     |  `----.|  |  |  |
// |__|     |_______||_______/__/ \__\ |_______/        \__/  \__/     |__|     |__|      \______||__|  |__|
//

// packetUtils.go
package policy

import (
	"l3/bgp/packet"
	"l3/bgp/utils"
	"strconv"
	utilspolicy "utils/policy"
	"utils/policy/policyCommonDefs"
)

func ApplyActionsToPacket(pa []packet.BGPPathAttr, stmt utilspolicy.PolicyStmt) []packet.BGPPathAttr {
	for _, action := range stmt.SetActionsState {
		utils.Logger.Infof("ApplyActionsToPacket - action:%+v", action)
		switch action.Attr {
		case policyCommonDefs.PolicyActionTypeSetLocalPref:
			pa = packet.SetLocalPrefToPathAttrs(pa, action.LocalPref, true)

		case policyCommonDefs.PolicyActionTypeSetCommunity:
			pa = packet.AddCommunityToPathAttrs(pa, action.Community)

		case policyCommonDefs.PolicyActionTypeSetExtendedCommunity:
			if extComm, err := strconv.ParseUint(action.ExtendedCommunity, 0, 64); err == nil {
				pa = packet.AddExtCommunityToPathAttrs(pa, extComm)
			} else {
				utils.Logger.Errf("ApplyActionsToPacket - Cannot convert ext community %v to uint",
					action.ExtendedCommunity)
			}
		}
	}
	return pa
}
