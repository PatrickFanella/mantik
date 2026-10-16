package investigation

import (
	"encoding/json"
	"errors"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const ProfileSchema = "mantik-review-profile-pin-v1"

type Profile struct {
	Schema   string              `json:"schema"`
	Contract Contract            `json:"contract"`
	Methods  []library.MethodRef `json:"methods"`
	SHA256   string              `json:"sha256"`
}

func ProfileFor(id string) (Profile, error) {
	c, err := DefinitionFor(id)
	if err != nil {
		return Profile{}, err
	}
	p := Profile{Schema: ProfileSchema, Contract: c, Methods: []library.MethodRef{}}
	if id == MethodTypeID {
		p.Methods, err = methodInventory()
		if err != nil {
			return Profile{}, err
		}
	}
	p.SHA256 = profileHash(p)
	return p, nil
}
func profileHash(p Profile) string {
	p.SHA256 = ""
	raw, _ := json.Marshal(p)
	return news.SHA256(string(raw))
}
func (p Profile) Validate() error {
	expected, err := ProfileFor(p.Contract.TypeID)
	if err != nil || !reflect.DeepEqual(p, expected) {
		return ErrProfile
	}
	return nil
}

var ErrProfile = errors.New("unsupported or changed retained review profile")
