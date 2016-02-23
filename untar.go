package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	dst := "."
	flag.StringVar(&dst, "C", dst, "directory to unpack to")
	flag.Parse()
	if dst == "" {
		dst = "."
	}
	if len(flag.Args()) != 1 {
		flag.Usage()
		os.Exit(1)
	}
	if err := openAndUntar(flag.Args()[0], dst); err != nil {
		log.Fatal(err)
	}
}

func openAndUntar(name, dst string) error {
	var rd io.Reader
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	rd = f
	if strings.HasSuffix(name, ".gz") || strings.HasSuffix(name, ".tgz") {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gr.Close()
		rd = gr
	}
	if err := os.MkdirAll(dst, os.ModeDir|os.ModePerm); err != nil {
		return err
	}
	// resetting umask is essential to have exact permissions on unpacked
	// files; it's not not put inside untar function as it changes
	// process-wide umask
	mask := unix.Umask(0)
	defer unix.Umask(mask)
	return untar(rd, dst)
}

func untar(f io.Reader, dst string) error {
	isRoot := os.Getuid() == 0
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		switch err {
		case nil:
		case io.EOF:
			return nil
		default:
			return err
		}
		name := filepath.Join(dst, filepath.Clean(hdr.Name))
		mode := hdr.FileInfo().Mode()
	ProcessHeader:
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			err = writeFile(name, mode, tr)
		case tar.TypeDir:
			switch err := os.Mkdir(name, mode); {
			case err == nil:
			case os.IsExist(err):
				if err := os.Chmod(name, mode); err != nil {
					return err
				}
			default:
				return err
			}
		case tar.TypeLink:
			err = os.Link(filepath.Join(dst, filepath.Clean(hdr.Linkname)), name)
		case tar.TypeSymlink:
			err = os.Symlink(filepath.Clean(hdr.Linkname), name)
		case tar.TypeFifo:
			err = unix.Mkfifo(name, syscallMode(mode))
		case tar.TypeChar, tar.TypeBlock:
			err = unix.Mknod(name, syscallMode(mode), devNo(hdr.Devmajor, hdr.Devminor))
		default:
			return fmt.Errorf("unsupported header type flag for %[2]q: %#[1]x (%[1]q)", hdr.Typeflag, hdr.Name)
		}
		if err != nil {
			if os.IsExist(err) {
				// if file already exists, try to remove it and
				// re-process — this is for everything except
				// directories and regular files
				if os.Remove(name) == nil {
					goto ProcessHeader
				}
			}
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeRegA, tar.TypeDir, tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
			if !hdr.AccessTime.IsZero() || !hdr.ModTime.IsZero() {
				now := time.Now()
				atime, mtime := hdr.AccessTime, hdr.ModTime
				// fix times that don't fit unix epoch
				if atime.UnixNano() < 0 {
					atime = now
				}
				if mtime.UnixNano() < 0 {
					mtime = now
				}
				if err := os.Chtimes(name, atime, mtime); err != nil {
					return err
				}
			}
			if isRoot {
				if err := os.Chown(name, hdr.Uid, hdr.Gid); err != nil {
					return err
				}
				// group change resets special attributes like
				// setgid, restore them
				if mode&os.ModeSetgid != 0 || mode&os.ModeSetuid != 0 {
					if err := os.Chmod(name, mode); err != nil {
						return err
					}
				}
			}
		}
	}
}

func writeFile(name string, fm os.FileMode, rd io.Reader) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fm)
	if err != nil {
		return err
	}
	defer f.Close()
	bufp := copyBufPool.Get().(*[]byte)
	defer copyBufPool.Put(bufp)
	if _, err := io.CopyBuffer(f, rd, *bufp); err != nil {
		return err
	}
	return f.Close()
}

// syscallMode returns the syscall-specific mode bits from Go's portable mode bits.
func syscallMode(i os.FileMode) (o uint32) {
	o |= uint32(i.Perm())
	if i&os.ModeSetuid != 0 {
		o |= unix.S_ISUID
	}
	if i&os.ModeSetgid != 0 {
		o |= unix.S_ISGID
	}
	if i&os.ModeSticky != 0 {
		o |= unix.S_ISVTX
	}
	if i&os.ModeNamedPipe != 0 {
		o |= unix.S_IFIFO
	}
	if i&os.ModeDevice != 0 {
		switch i & os.ModeCharDevice {
		case 0:
			o |= unix.S_IFBLK
		default:
			o |= unix.S_IFCHR
		}
	}
	return
}

var copyBufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 512*1024)
		return &b
	},
}

func init() { log.SetFlags(0) }
