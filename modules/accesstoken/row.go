//go:build !lite

package accesstoken

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/store"
)

func NewRow(name, note string, channelIDs []string) (plain string, row store.AccessTokenRow, err error) {
	plain, err = Generate()
	if err != nil {
		return "", store.AccessTokenRow{}, err
	}
	id, err := randomID()
	if err != nil {
		return "", store.AccessTokenRow{}, err
	}
	row = store.AccessTokenRow{
		ID: id, Name: strings.TrimSpace(name), TokenHash: Hash(plain), Prefix: Prefix(plain),
		ScopeJSON: EncodeScope(channelIDs), Enabled: true, Note: note, CreatedAt: time.Now().Unix(),
	}
	if row.Name == "" {
		row.Name = "link"
	}
	return plain, row, nil
}

func randomID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
