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

package etcd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/sirupsen/logrus"

	"github.com/erda-project/erda/pkg/jsonstore/etcd"
	"github.com/erda-project/erda/pkg/kms/kmstypes"
)

const (
	EnvKeyEtcdEndpoints = "ETCD_ENDPOINTS"
)

func isNotFoundErr(err error) bool {
	return err.Error() == "not found"
}

type Store struct {
	etcdClient *etcd.Store
}

func init() {
	logrus.Infof("begin register etcd store createFn to factory...")
	err := kmstypes.RegisterStore(kmstypes.StoreKind_ETCD, func(ctx context.Context) kmstypes.Store {
		configMapValue := ctx.Value(kmstypes.CtxKeyConfigMap)
		if configMapValue == nil {
			panic("failed to init etcd store, no configs passed in")
		}
		configMap := configMapValue.(map[string]string)
		endpoints, ok := configMap[EnvKeyEtcdEndpoints]
		if !ok || endpoints == "" {
			panic("failed to init etcd store, missing env ETCD_ENDPOINTS")
			return nil
		}
		etcdclient, err := etcd.New(etcd.WithEndpoints(strings.Split(endpoints, ",")))
		if err != nil {
			panic(fmt.Errorf("failed to init etcd client, err: %v", err))
			return nil
		}

		s := Store{etcdClient: etcdclient}

		return &s
	})
	if err != nil {
		logrus.Errorf("[alert] failed to register etcd store createFn to factory, err: %v", err)
		os.Exit(1)
	}
}

func (s *Store) GetKind() kmstypes.StoreKind {
	return kmstypes.StoreKind_ETCD
}

func (s *Store) CreateKey(keyInfo kmstypes.KeyInfo) error {
	ctx := context.Background()

	err := kmstypes.CheckKeyForCreate(keyInfo)
	if err != nil {
		return err
	}

	now := time.Now()
	keyVersion := kmstypes.KeyVersion{
		VersionID:          keyInfo.GetPrimaryKeyVersion().GetVersionID(),
		SymmetricKeyBase64: keyInfo.GetPrimaryKeyVersion().GetSymmetricKeyBase64(),
		CreatedAt:          &now,
		UpdatedAt:          &now,
	}
	keyVersionJSON, err := json.Marshal(&keyVersion)
	if err != nil {
		return err
	}
	key := kmstypes.Key{
		PluginKind:        keyInfo.GetPluginKind(),
		KeyID:             keyInfo.GetKeyID(),
		PrimaryKeyVersion: keyVersion,
		KeySpec:           keyInfo.GetKeySpec(),
		KeyUsage:          keyInfo.GetKeyUsage(),
		KeyState:          keyInfo.GetKeyState(),
		Description:       keyInfo.GetDescription(),
		CreatedAt:         &now,
		UpdatedAt:         &now,
	}
	keyJSON, err := json.Marshal(&key)
	if err != nil {
		return err
	}
	resp, err := s.etcdClient.GetClient().Txn(ctx).
		Then(
			// CMK
			// 主数据
			clientv3.OpPut(makeEtcdKeyID(keyInfo.GetKeyID()), string(keyJSON)),
			// 引用：插件类型
			clientv3.OpPut(makeEtcdKeyIDUnderPlugin(keyInfo.GetKeyID(), key.GetPluginKind()), makeEtcdKeyID(keyInfo.GetKeyID())),

			// PrimaryKeyVersion
			clientv3.OpPut(makeEtcdKeyVersionID(keyInfo.GetKeyID(), keyInfo.GetPrimaryKeyVersion().GetVersionID()), string(keyVersionJSON)),
		).
		Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return fmt.Errorf("failed to put data into etcd when create key")
	}

	return nil
}

func (s *Store) GetKey(keyID string) (kmstypes.KeyInfo, error) {
	ctx := context.Background()
	key, err := getKeyFromEtcd(ctx, keyID, s.etcdClient)
	if err != nil {
		if isNotFoundErr(err) {
			return nil, fmt.Errorf("key not exist")
		}
		return nil, fmt.Errorf("get key from etcd failed, err: %v", err)
	}
	return key, nil
}

func (s *Store) ListKeysByKind(kind kmstypes.PluginKind) ([]string, error) {
	ctx := context.Background()
	values, err := s.etcdClient.PrefixGet(ctx, makeEtcdPluginKindPrefix(kind))
	if err != nil {
		return nil, err
	}
	var keys []string
	prefix := makeEtcdPluginKindPrefix(kind)
	for _, v := range values {
		keys = append(keys, strings.TrimPrefix(string(v.Value), prefix))
	}
	return keys, nil
}

func (s *Store) DeleteByKeyID(keyID string) error {
	ctx := context.Background()
	_, err := s.etcdClient.Remove(ctx, makeEtcdKeyID(keyID))
	// TODO delete relation key
	return err
}

func (s *Store) GetKeyVersion(keyID, keyVersionID string) (kmstypes.KeyVersionInfo, error) {
	ctx := context.Background()
	value, err := s.etcdClient.Get(ctx, makeEtcdKeyVersionID(keyID, keyVersionID))
	if err != nil {
		if isNotFoundErr(err) {
			return nil, fmt.Errorf("key version not exist")
		}
		return nil, err
	}
	var keyVersion kmstypes.KeyVersion
	if err := json.Unmarshal(value.Value, &keyVersion); err != nil {
		return nil, err
	}
	return &keyVersion, nil
}

func (s *Store) RotateKeyVersion(keyID string, newKeyVersionInfo kmstypes.KeyVersionInfo) (kmstypes.KeyVersionInfo, error) {
	ctx := context.Background()
	now := time.Now()

	// new keyVersionInfo
	newKeyVersionInfo.SetCreatedAt(now)
	newKeyVersionInfo.SetUpdatedAt(now)
	// keyInfo
	keyInfo, err := s.GetKey(keyID)
	if err != nil {
		return nil, err
	}
	keyInfo.SetPrimaryKeyVersion(newKeyVersionInfo)
	keyInfo.SetUpdatedAt(now)

	keyJSON, err := json.Marshal(keyInfo)
	if err != nil {
		return nil, err
	}
	newKeyVersionJSON, err := json.Marshal(newKeyVersionInfo)
	if err != nil {
		return nil, err
	}

	resp, err := s.etcdClient.GetClient().Txn(ctx).
		Then(
			// New Key Version
			clientv3.OpPut(makeEtcdKeyVersionID(keyID, newKeyVersionInfo.GetVersionID()), string(newKeyVersionJSON)),

			// Update CMK PrimaryKeyVersion
			clientv3.OpPut(makeEtcdKeyID(keyID), string(keyJSON)),
		).
		Commit()
	if err != nil {
		return nil, err
	}
	if !resp.Succeeded {
		return nil, fmt.Errorf("failed to put data into etcd when rotate key version")
	}
	err = s.etcdClient.Put(ctx, makeEtcdKeyVersionID(keyID, newKeyVersionInfo.GetVersionID()), string(newKeyVersionJSON))
	if err != nil {
		return nil, err
	}

	return newKeyVersionInfo, nil
}

func makeEtcdKeyID(keyID string) string {
	return fmt.Sprintf("/dice/kms/cmk/%s", keyID)
}

func makeEtcdKeyIDUnderPlugin(keyID string, pluginKind kmstypes.PluginKind) string {
	return makeEtcdPluginKindPrefix(pluginKind) + keyID
}

func makeEtcdPluginKindPrefix(pluginKind kmstypes.PluginKind) string {
	return fmt.Sprintf("/dice/kms/cmk/%s/", pluginKind)
}

func makeEtcdKeyVersionID(keyID, keyVersion string) string {
	return fmt.Sprintf("%s/version/%s", makeEtcdKeyID(keyID), keyVersion)
}

func getKeyFromEtcd(ctx context.Context, keyID string, etcdClient *etcd.Store) (*kmstypes.Key, error) {
	value, err := etcdClient.Get(ctx, makeEtcdKeyID(keyID))
	if err != nil {
		return nil, err
	}
	var model kmstypes.Key
	if err := json.Unmarshal(value.Value, &model); err != nil {
		return nil, err
	}
	return &model, nil
}
