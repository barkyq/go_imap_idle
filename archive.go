package main

import "flag"
import "os"
import "io/fs"
import "io"
import "path/filepath"
import "crypto/sha256"
import "fmt"

var archive_flag = flag.Bool("a", false, "archive mode")
var reverse_mode = flag.Bool("r", false, "reverse mode of operation")

func save_to_archive(targetdir string, r io.Reader, original string) error {
	hash := sha256.New()
	buffer := make([]byte, 1024)
	var digest [32]byte
	if _, e := io.CopyBuffer(hash, r, buffer); e != nil {
		return e
	}
	copy(digest[:], hash.Sum(nil))
	hash.Reset()

	first_byte := fmt.Sprintf("%02x", digest[0])
	rest_bytes := fmt.Sprintf("%02x", digest[1:])
	if i, e := os.Stat(filepath.Join(targetdir, first_byte)); e != nil {
		if e := os.MkdirAll(filepath.Join(targetdir, first_byte), os.ModePerm); e != nil {
			return e
		}
	} else if !i.IsDir() {
		return fmt.Errorf("invalid target directory structure")
	}
	if e := os.Rename(original, filepath.Join(targetdir, first_byte, rest_bytes)); e != nil {
		return e
	} else {
		return nil
	}
}

func reverse(sourcedir, targetdir string) error {
	file_tree := os.DirFS(targetdir)
	buffer := make([]byte, 1024)
	return fs.WalkDir(file_tree, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}
		if w, e := os.Create(filepath.Join(sourcedir, "cur", filepath.Dir(path)+filepath.Base(path))); e != nil {
			return e
		} else if r, e := file_tree.Open(path); e != nil {
			return e
		} else if _, e := io.CopyBuffer(w, r, buffer); e != nil {
			return e
		}
		return nil
	})
}

func forward(sourcedir, targetdir string) error {
	if i, e := os.Stat(sourcedir); e != nil || !i.IsDir() {
		return fmt.Errorf("set the -s option")
	}

	hash := sha256.New()
	buffer := make([]byte, 1024)
	var first_byte string
	var rest_bytes string
	var digest [32]byte
	file_tree := os.DirFS(sourcedir)
	return fs.WalkDir(file_tree, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}
		if f, e := file_tree.Open(path); e == nil {
			if _, e := io.CopyBuffer(hash, f, buffer); e != nil {
				return e
			}
			copy(digest[:], hash.Sum(nil))
			hash.Reset()
			f.Close()
			first_byte = fmt.Sprintf("%02x", digest[0])
			rest_bytes = fmt.Sprintf("%02x", digest[1:])
			if i, e := os.Stat(filepath.Join(targetdir, first_byte)); e != nil {
				if e := os.MkdirAll(filepath.Join(targetdir, first_byte), os.ModePerm); e != nil {
					return e
				}
			} else if !i.IsDir() {
				return fmt.Errorf("invalid target directory structure")
			}
			if e := os.Rename(filepath.Join(sourcedir, path), filepath.Join(targetdir, first_byte, rest_bytes)); e != nil {
				return e
			} else {
				return nil
			}
		} else {
			return e
		}
	})
}
