package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

const NativeMethodSchema = "native-strategy-config-v1"

type MethodRef struct {
	Schema string `json:"schema"`
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}
type NativeMethod struct {
	Schema   string                 `json:"schema"`
	Entry    Entry                  `json:"entry"`
	Strategy domain.StrategyVersion `json:"strategy"`
}

// PinMethod binds the immutable compiled strategy and mandatory library contract.
// Reading a pin neither runs its checks nor qualifies the method.
func PinMethod(id string) (MethodRef, NativeMethod, error) {
	strategy, err := Strategy(id)
	if err != nil {
		return MethodRef{}, NativeMethod{}, err
	}
	entry, err := ResolveStrategy(strategy)
	if err != nil {
		return MethodRef{}, NativeMethod{}, err
	}
	method := NativeMethod{NativeMethodSchema, entry, strategy}
	raw, err := json.Marshal(method)
	if err != nil {
		return MethodRef{}, NativeMethod{}, err
	}
	sum := sha256.Sum256(raw)
	return MethodRef{NativeMethodSchema, id, hex.EncodeToString(sum[:])}, method, nil
}
