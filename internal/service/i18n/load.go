// Package i18n — loading a catalogue directory through the codec domain.
package i18n

import (
	"io/fs"
	"path"

	"github.com/kitsunium/sdk/internal/core/codec"
	corei18n "github.com/kitsunium/sdk/internal/core/i18n"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// expectedCatalogueKeys sizes the decode map. It is a HINT and never a bound:
// a catalogue with more keys grows the map exactly as it would have without
// it, and one with fewer wastes a few buckets at startup. Sixty-four is a
// small application's whole message set and a large one's first page.
const expectedCatalogueKeys int = 64

// LoadFS reads one catalogue file per language from dir and returns a [Store].
//
// # It invents no file format
//
// The bytes are decoded by the codec domain — [codec.Lookup] on the format the
// caller names — so a catalogue is JSON, YAML, TOML, CBOR, MessagePack or any
// of the other formats internal/service/codec registers, and this package
// contains no parser. The codec must be registered: blank-import pkg/v1/codec,
// exactly as internal/service/config.FileSource requires. An unregistered
// format is [CatalogLoadFailed] rather than a panic or a silent empty
// catalogue.
//
// # It invents no filesystem either
//
// fsys is io/fs.FS, which is internal/core/vfs.FS unchanged — the ADR 0056
// alias — so an embed.FS, an os.DirFS, a vfs.NewOS root and a vfs.NewMem all
// work here with no adapter and this package imports neither vfs nor os. A
// catalogue compiled into the binary and a catalogue on disk are the same
// call.
//
// # The tag comes from the file NAME
//
// "en.json" is English, "pt-PT.yaml" is European Portuguese. The base name
// minus its extension is parsed by [corei18n.ParseTag], so the whole refusal
// list of that function applies to filenames: "fr_FR.json" is refused, because
// accepting it would mint a second Tag for French and split the catalogue.
// Subdirectories are ignored rather than walked — a nested layout is a
// convention this package does not own, and walking one would silently pick up
// files nobody meant as catalogues.
//
// Two files that canonicalise to one tag ("EN.json" and "en.json") are
// [CatalogInvalid]: one would win, the choice would depend on directory order,
// and half the strings would vanish.
func LoadFS(fsys fs.FS, dir string, format codec.Format, fallback corei18n.TagValue) (store *Store, err error) {
	//: resolve the codec before touching the filesystem, so an unregistered
	//: format is reported as the wiring fault it is rather than after a
	//: directory read that was never going to be usable.
	decoder, ok := codec.Lookup(format)
	//: an unregistered format cannot decode anything.
	if !ok {
		//: name the format; the fix is a blank import.
		return nil, errs.Wrap(CatalogLoadFailed, errs.WrapParams{},
			errs.String("format", string(format)), errs.String("detail", "no codec registered for this format — blank-import pkg/v1/codec"))
	}
	//: list the directory.
	entries, err := fs.ReadDir(fsys, dir)
	//: an unreadable directory is a load failure, cause preserved so
	//: errors.Is against fs.ErrNotExist keeps answering.
	if err != nil {
		//: CatalogLoadFailed, with the *fs.PathError intact underneath.
		return nil, failLoad(err, errs.String("dir", dir))
	}
	//: decode each file into a catalogue.
	catalogues, err := readCatalogues(fsys, dir, entries, decoder)
	//: a defect in any file refuses the whole load.
	if err != nil {
		//: already carries the path.
		return nil, err
	}
	//: NewStore performs every semantic check, so LoadFS owns only the I/O
	//: and the decode — one place to look for each kind of failure.
	return NewStore(fallback, catalogues)
}

// readCatalogues decodes every regular file in entries into a catalogue.
func readCatalogues(fsys fs.FS, dir string, entries []fs.DirEntry, decoder codec.Codec) (catalogues map[corei18n.TagValue]Catalogue, err error) {
	//: one catalogue per language.
	catalogues = make(map[corei18n.TagValue]Catalogue, len(entries))
	//: fs.ReadDir returns entries sorted by filename, so this walk is already
	//: deterministic and the FIRST defect reported is stable across runs.
	for _, entry := range entries {
		//: a subdirectory is not a catalogue and is not walked.
		if entry.IsDir() {
			//: skip.
			continue
		}
		//: the tag is the base name without its extension.
		tag, tagErr := tagFromFilename(entry.Name())
		//: a filename that is not a tag is a catalogue defect, named.
		if tagErr != nil {
			//: InvalidTag, with the file that carried it.
			return nil, tagErr
		}
		//: two files for one language would silently drop half the strings.
		if _, duplicate := catalogues[tag]; duplicate {
			//: refuse rather than let directory order decide.
			return nil, errs.Wrap(CatalogInvalid, errs.WrapParams{},
				errs.String("tag", tag.String()), errs.String("file", entry.Name()), errs.String("detail", "two files resolve to the same language tag"))
		}
		//: read and decode it.
		catalogue, fileErr := readCatalogue(fsys, path.Join(dir, entry.Name()), decoder)
		//: an unreadable or undecodable file refuses the load.
		if fileErr != nil {
			//: already carries the path.
			return nil, fileErr
		}
		//: keep it under its language.
		catalogues[tag] = catalogue
	}
	//: every file decoded.
	return catalogues, nil
}

// tagFromFilename parses the base name of a catalogue file into a [Tag].
func tagFromFilename(name string) (tag corei18n.TagValue, err error) {
	//: strip the extension; "en.json" names English.
	base := name[:len(name)-len(path.Ext(name))]
	//: the whole ParseTag refusal list applies to filenames.
	tag, err = corei18n.ParseTag(base)
	//: a filename that is not a tag in the subset.
	if err != nil {
		//: add the file so the maintainer knows which one to rename.
		return corei18n.TagValue{}, errs.Wrap(err, errs.WrapParams{}, errs.String("file", name))
	}
	//: the language this file translates into.
	return tag, nil
}

// readCatalogue reads one file and converts its decoded shape into a
// [Catalogue].
func readCatalogue(fsys fs.FS, name string, decoder codec.Codec) (catalogue Catalogue, err error) {
	//: read the bytes.
	data, err := fs.ReadFile(fsys, name)
	//: an unreadable file is a load failure, cause preserved.
	if err != nil {
		//: CatalogLoadFailed, with the *fs.PathError intact underneath.
		return nil, failLoad(err, errs.String("file", name))
	}
	//: decode into the format-neutral shape every codec can produce. A typed
	//: struct would need per-format tags, and the two entry SHAPES — a string
	//: or a map — cannot both land on one Go field anyway.
	raw := make(map[string]any, expectedCatalogueKeys)
	//: the codec owns the parse; this package owns nothing about the syntax.
	//: note that a codec returning its OWN typed error keeps its code at the
	//: top under errs origin-wins, and CodeCatalogLoadFailed lands in the
	//: trail — which is the better outcome, since "invalid character '}' at
	//: offset 18" is what a maintainer needs, and errs.HasCode still answers
	//: for both codes.
	if decodeErr := decoder.Unmarshal(data, &raw); decodeErr != nil {
		//: CatalogLoadFailed, with the codec's own typed error underneath.
		return nil, failLoad(decodeErr, errs.String("file", name))
	}
	//: convert the decoded shape into entries.
	return entriesFrom(name, raw)
}

// entriesFrom converts a decoded catalogue file into [Entry] values.
func entriesFrom(file string, raw map[string]any) (catalogue Catalogue, err error) {
	//: one entry per key.
	catalogue = make(Catalogue, len(raw))
	//: convert each.
	for key, value := range raw {
		//: a string is an uncounted message; a map is a counted one.
		entry, entryErr := entryFrom(file, key, value)
		//: any other shape is refused, naming the key and the shape.
		if entryErr != nil {
			//: CatalogInvalid.
			return nil, entryErr
		}
		//: keep it.
		catalogue[corei18n.Key(key)] = entry
	}
	//: the file's entries.
	return catalogue, nil
}

// entryFrom converts one decoded value into an [Entry].
//
// Exactly two shapes are accepted. Anything else — a number, a boolean, a
// list, a nested map — is refused BY SHAPE rather than coerced, because every
// coercion here has a plausible wrong answer: a YAML `1.0` is a float and
// would become "1", a `no` is a boolean and would become "false", and both
// would render as a translation the file does not contain.
func entryFrom(file, key string, value any) (entry EntryValue, err error) {
	//: the uncounted shape.
	if text, ok := value.(string); ok {
		//: one pattern, no completeness question.
		return Plain(text), nil
	}
	//: the counted shape, as most codecs decode a nested object.
	if forms, ok := value.(map[string]any); ok {
		//: every member must itself be a string pattern.
		return countedEntry(file, key, forms)
	}
	//: the counted shape, as a codec that decodes homogeneous maps produces.
	if forms, ok := value.(map[string]string); ok {
		//: already the right shape.
		return PluralForms(forms), nil
	}
	//: neither shape — the TYPE is named, the value never is.
	return EntryValue{}, errs.Wrap(CatalogInvalid, errs.WrapParams{},
		errs.String("file", file), errs.String("key", key),
		errs.String("detail", "entry is neither a pattern nor a map of CLDR forms"))
}

// countedEntry converts a decoded nested object into a counted [Entry].
func countedEntry(file, key string, forms map[string]any) (entry EntryValue, err error) {
	//: one pattern per declared category.
	out := make(map[string]string, len(forms))
	//: every member must be a string.
	for name, value := range forms {
		//: a non-string member is refused rather than formatted.
		text, ok := value.(string)
		//: refuse.
		if !ok {
			//: name the key and the category, never the value.
			return EntryValue{}, errs.Wrap(CatalogInvalid, errs.WrapParams{},
				errs.String("file", file), errs.String("key", key), errs.String("form", name),
				errs.String("detail", "a plural form must be a pattern string"))
		}
		//: keep it under its CLDR name; NewStore validates the name itself.
		out[name] = text
	}
	//: a counted entry.
	return PluralForms(out), nil
}
