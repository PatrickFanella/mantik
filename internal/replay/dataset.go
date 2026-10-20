// Package replay runs isolated fixture projections using shared dataset contracts.
package replay

import (
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
)

const DatasetSchema = dataset.DatasetSchema

type Mapping = dataset.Mapping
type Access = dataset.Access
type Record = dataset.Record
type Dataset = dataset.Dataset

func Digest(raw string) string                { return dataset.Digest(raw) }
func Seal(d Dataset) (Dataset, error)         { return dataset.Seal(d) }
func Read(input io.Reader) (Dataset, error)   { return dataset.Read(input) }
func Decode(input io.Reader) (Dataset, error) { return dataset.Decode(input) }
func FixtureDataset() (Dataset, error)        { return dataset.FixtureDataset() }
