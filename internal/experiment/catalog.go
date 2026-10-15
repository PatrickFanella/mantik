package experiment

import "git.subcult.tv/subculture-collective/mantik/internal/experimentplan"

const CatalogSchema = experimentplan.CatalogSchema

type Catalog = experimentplan.Catalog

func BuiltinCatalog() (Catalog, error) { return experimentplan.BuiltinCatalog() }
