// Package hypothesis retains research declarations and their publication lineage.
package hypothesis

import (
	"encoding/json"
	"io"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

const Schema = question.Schema
const ResultSchema = question.ResultSchema
const MethodSchema = question.MethodSchema
const MaxBytes = question.MaxBytes

var ErrInvalid = question.ErrInvalid
var field = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type Mechanism = question.Mechanism
type Condition = question.Condition
type MethodRef = question.MethodRef
type Origin = question.Origin
type Declaration = question.Declaration
type NativeMethod = question.NativeMethod
type Result = question.Result

func PinMethod(id string) (MethodRef, NativeMethod, error) { return question.PinMethod(id) }
func Seal(d Declaration) (Result, error)                   { return question.Seal(d) }
func Fixture() Declaration                                 { return question.Fixture() }
func Read(r io.Reader) (Declaration, error)                { return question.Read(r) }

func unique(d *json.Decoder, depth int) error {
	if depth > 16 {
		return ErrInvalid
	}
	token, err := d.Token()
	if err != nil {
		return ErrInvalid
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return ErrInvalid
				}
				name, ok := k.(string)
				if !ok || !field.MatchString(name) || seen[name] {
					return ErrInvalid
				}
				seen[name] = true
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := unique(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
		if _, err = d.Token(); err != nil {
			return ErrInvalid
		}
	}
	return nil
}
