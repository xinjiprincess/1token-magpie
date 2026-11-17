package provider

import (
	"errors"
	"os"
)

// ReadModelAppTransfer reads exactly one explicitly selected client's model
// configuration. It does not discover accounts, save providers or import OAuth.
func ReadModelAppTransfer(id string) (AppSource, error) {
	for _, reader := range appReaders {
		if reader.id != id {
			continue
		}
		out := AppSource{ID: reader.id, Name: reader.name, Path: reader.path(), Items: []AppImport{}}
		if _, err := os.Stat(out.Path); errors.Is(err, os.ErrNotExist) {
			return out, nil
		} else if err != nil {
			return out, err
		}
		out.Found = true
		items, err := reader.read(out.Path)
		if err != nil {
			return out, err
		}
		out.Items = items
		return out, nil
	}
	return AppSource{}, errors.New("unsupported model configuration source")
}
