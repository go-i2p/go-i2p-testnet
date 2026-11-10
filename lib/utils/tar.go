package utils

import (
	"archive/tar"
	"bytes"
	"io"

	"go-i2p-testnet/lib/utils/logger"
)

var log = logger.GetTestnetLogger()

func CreateTarArchive(filename, content string) (io.Reader, error) {
	log.WithFields(map[string]interface{}{
		"filename":    filename,
		"contentSize": len(content),
	}).Debug("Starting tar archive creation")

	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)

	hdr := &tar.Header{
		Name: filename,
		Mode: 0o600,
		Size: int64(len(content)),
	}

	log.WithFields(map[string]interface{}{
		"filename": filename,
		"mode":     hdr.Mode,
		"size":     hdr.Size,
	}).Debug("Writing tar header")

	if err := tw.WriteHeader(hdr); err != nil {
		log.WithFields(map[string]interface{}{
			"filename": filename,
			"error":    err,
		}).Error("Failed to write tar header")
		return nil, err
	}
	log.Debug("Writing content to tar archive")
	if _, err := tw.Write([]byte(content)); err != nil {
		log.WithFields(map[string]interface{}{
			"filename": filename,
			"error":    err,
		}).Error("Failed to write content to tar archive")
		return nil, err
	}
	log.Debug("Closing tar writer")
	if err := tw.Close(); err != nil {
		log.WithFields(map[string]interface{}{
			"filename": filename,
			"error":    err,
		}).Error("Failed to close tar writer")
		return nil, err
	}

	log.WithFields(map[string]interface{}{
		"filename": filename,
		"size":     buf.Len(),
	}).Debug("Successfully created tar archive")

	return buf, nil
}
