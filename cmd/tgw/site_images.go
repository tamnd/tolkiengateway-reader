package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// imageBundle is the set of downloaded wiki images the site carries. Every
// other image is hotlinked from the wiki, so the bundle can be cut to fit
// a size budget without breaking a page.
type imageBundle struct {
	dir   string
	set   map[string]int64 // path below /w/images/ → size
	bytes int64
}

// has reports whether an image is bundled. A nil bundle has nothing.
func (b *imageBundle) has(rel string) bool {
	if b == nil {
		return false
	}
	_, ok := b.set[rel]
	return ok
}

// planImages picks the images to bundle: the ones the site shows, most
// used first, until the budget runs out. Pages display thumbnails, so
// originals are only bundled where a page shows the original itself.
func planImages(s *site, dir string, budget int64) *imageBundle {
	b := &imageBundle{dir: dir, set: map[string]int64{}}
	if dir == "" {
		return b
	}
	uses := map[string]int{}
	for _, p := range s.pages {
		if p.Meta.Redirect {
			continue
		}
		for _, rel := range p.thumbs {
			uses[rel]++
		}
		if f := p.Meta.File; f != nil && p.Meta.Namespace == 6 {
			name := strings.TrimPrefix(p.Meta.Title, "File:")
			weight := 1 + len(s.fileUsage[strings.ReplaceAll(name, " ", "_")])
			for _, t := range fileDisplayThumbs(f.URL, f.Mime, f.Width, f.Height) {
				uses[t.rel] += weight
			}
		}
	}
	rels := make([]string, 0, len(uses))
	for rel := range uses {
		rels = append(rels, rel)
	}
	sort.Slice(rels, func(i, j int) bool {
		if uses[rels[i]] != uses[rels[j]] {
			return uses[rels[i]] > uses[rels[j]]
		}
		return rels[i] < rels[j]
	})
	for _, rel := range rels {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() {
			continue
		}
		if budget > 0 && b.bytes+info.Size() > budget {
			continue
		}
		b.set[rel] = info.Size()
		b.bytes += info.Size()
	}
	return b
}

// copy places the bundled images below out, hard linked where the file
// system allows it, and returns how many there are.
func (b *imageBundle) copy(out string) (int, error) {
	if b == nil || len(b.set) == 0 {
		return 0, nil
	}
	rels := make([]string, 0, len(b.set))
	for rel := range b.set {
		rels = append(rels, rel)
	}
	errs := make([]error, len(rels))
	parallelFor(len(rels), func(i int) {
		src := filepath.Join(b.dir, filepath.FromSlash(rels[i]))
		dst := filepath.Join(out, filepath.FromSlash(rels[i]))
		errs[i] = linkOrCopy(src, dst)
	})
	for _, err := range errs {
		if err != nil {
			return 0, err
		}
	}
	return len(rels), nil
}

func linkOrCopy(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if info, err := os.Stat(dst); err == nil {
		if srcInfo, err := os.Stat(src); err == nil && os.SameFile(info, srcInfo) {
			return nil
		}
		os.Remove(dst)
	}
	if os.Link(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	outFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(outFile, in); err != nil {
		outFile.Close()
		return err
	}
	return outFile.Close()
}

// thumbSpec is one rendition of a file: its path below /w/images/ and the
// size it is shown at.
type thumbSpec struct {
	rel           string
	width, height int
}

// thumbnailable are the types MediaWiki renders scaled copies of.
var thumbnailable = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true, "image/svg+xml": true,
}

// scaledImage is the rendition MediaWiki serves for a file shown width
// pixels wide: a thumbnail, or the original when that is no larger.
func scaledImage(origRel, mime string, origWidth, origHeight, width int) thumbSpec {
	height := origHeight
	if origWidth > 0 {
		height = int(math.Round(float64(origHeight) * float64(width) / float64(origWidth)))
	}
	if mime != "image/svg+xml" && width >= origWidth {
		return thumbSpec{rel: origRel, width: origWidth, height: origHeight}
	}
	name := path.Base(origRel)
	thumb := fmt.Sprintf("thumb/%s/%dpx-%s", origRel, width, name)
	if mime == "image/svg+xml" {
		thumb += ".png"
	}
	return thumbSpec{rel: thumb, width: width, height: height}
}

// fitBoxWidth is MediaWiki's File::scaleHeight inverse: the widest width
// whose scaled height still fits maxHeight.
func fitBoxWidth(width, height, maxHeight int) int {
	ideal := float64(width) * float64(maxHeight) / float64(height)
	rounded := int(math.Ceil(ideal))
	if int(math.Round(float64(rounded)*float64(height)/float64(width))) > maxHeight {
		return int(math.Floor(ideal))
	}
	return rounded
}

// fitBox scales a file to fit a box, never enlarging it.
func fitBox(width, height, maxWidth, maxHeight int) int {
	if width <= maxWidth && height <= maxHeight {
		return width
	}
	if float64(width)/float64(height) >= float64(maxWidth)/float64(maxHeight) {
		return maxWidth
	}
	return fitBoxWidth(width, height, maxHeight)
}

// filePreview is the image a file page shows, sized as ImagePage does for
// the default 800×600 limit.
func filePreview(origRel, mime string, width, height int) thumbSpec {
	w := width
	if width > 800 || height > 600 {
		if float64(width)/float64(height) >= 800.0/600.0 {
			w = 800
		} else {
			w = int(math.Floor(float64(width) * 600 / float64(height)))
		}
	}
	return scaledImage(origRel, mime, width, height, w)
}

// galleryThumb is a file in a category's media gallery and in the file
// history table, both of which fit files into 120×120.
func galleryThumb(origRel, mime string, width, height int) thumbSpec {
	return scaledImage(origRel, mime, width, height, fitBox(width, height, 120, 120))
}

// fileDisplayThumbs are the renditions the site shows for a file of its
// own accord, beyond what the wiki's pages embed: the file page preview
// and the gallery thumbnail. tgw crawl images downloads them too.
func fileDisplayThumbs(url, mime string, width, height int) []thumbSpec {
	if !thumbnailable[mime] || width <= 0 || height <= 0 {
		return nil
	}
	rel, ok := imagePath(url)
	if !ok {
		return nil
	}
	return []thumbSpec{filePreview(rel, mime, width, height), galleryThumb(rel, mime, width, height)}
}
