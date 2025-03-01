// Copyright (c) 2021 Terminus, Inc.
//
// This program is free software: you can use, redistribute, and/or modify
// it under the terms of the GNU Affero General Public License, version 3
// or later ("AGPL"), as published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful, but WITHOUT
// ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or
// FITNESS FOR A PARTICULAR PURPOSE.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <http://www.gnu.org/licenses/>.

package fileDetail

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirupsen/logrus"

	"github.com/erda-project/erda/apistructs"
	protocol "github.com/erda-project/erda/modules/openapi/component-protocol"
)

type ComponentAction struct {
	ctxBdl protocol.ContextBundle

	State State                  `json:"state"`
	Props map[string]interface{} `json:"props"`
}

type State struct {
	ActiveKey  apistructs.TestPlanActiveKey `json:"activeKey"`
	TestPlanId uint64                       `json:"testPlanId"`
	SpaceId    uint64                       `json:"spaceId"`
}

func (ca *ComponentAction) SetCtxBundle(b protocol.ContextBundle) error {
	if b.Bdl == nil || b.I18nPrinter == nil {
		err := fmt.Errorf("invalie context bundle")
		return err
	}
	logrus.Infof("inParams:%+v, identity:%+v", b.InParams, b.Identity)
	ca.ctxBdl = b
	return nil
}

func (ca *ComponentAction) RenderState(c *apistructs.Component) error {
	var state State
	b, err := json.Marshal(c.State)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	ca.State = state
	return nil
}

func (ca *ComponentAction) Render(ctx context.Context, c *apistructs.Component, scenario apistructs.ComponentProtocolScenario, event apistructs.ComponentEvent, gs *apistructs.GlobalStateData) error {
	bdl := ctx.Value(protocol.GlobalInnerKeyCtxBundle.String()).(protocol.ContextBundle)
	if err := ca.SetCtxBundle(bdl); err != nil {
		return err
	}
	if c.State == nil {
		c.State = map[string]interface{}{}
	}

	if err := ca.RenderState(c); err != nil {
		return err
	}
	ca.State.TestPlanId = uint64(ca.ctxBdl.InParams["testPlanId"].(float64))
	// props
	ca.Props = make(map[string]interface{})
	ca.Props["tabMenu"] = []map[string]string{
		{"key": apistructs.ConfigTestPlanActiveKey.String(), "name": "配置信息"},
		{"key": apistructs.ExecuteTestPlanActiveKey.String(), "name": "执行明细"},
	}
	switch event.Operation {
	case apistructs.InitializeOperation:
		ca.State.ActiveKey = apistructs.ConfigTestPlanActiveKey
		plan, err := ca.ctxBdl.Bdl.GetTestPlanV2(ca.State.TestPlanId)
		if err != nil {
			return err
		}
		ca.State.SpaceId = plan.Data.SpaceID
	case "changeActiveKey":
		ca.State.ActiveKey = c.State["activeKey"].(apistructs.TestPlanActiveKey)
	}

	// set state
	err := ca.marshal(c)
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(`{"onChange":{"key":"changeViewType","reload":true}}`), &c.Operations)
}

func RenderCreator() protocol.CompRender {
	return &ComponentAction{}
}

func (ca *ComponentAction) marshal(c *apistructs.Component) error {
	// state
	stateValue, err := json.Marshal(ca.State)
	if err != nil {
		return err
	}
	var stateMap map[string]interface{}
	err = json.Unmarshal(stateValue, &stateMap)
	if err != nil {
		return err
	}
	c.State = stateMap
	//props
	c.Props = ca.Props
	return nil
}
