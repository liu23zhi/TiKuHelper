package service

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
)

const maxExtensionBytes = 32 << 20

type extensionManifest struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	ManifestVersion int    `json:"manifest_version"`
}

type crxField struct {
	number uint64
	data   []byte
}

// CRX3 signs the signed-header length, signed header, and ZIP together. A valid
// matching developer proof binds the requested store ID to the whole archive.
func validateExtensionCRX(data []byte, expectedID string) (extensionManifest, error) {
	var manifest extensionManifest
	if len(data) < 12 || len(data) > maxExtensionBytes || string(data[:4]) != "Cr24" || binary.LittleEndian.Uint32(data[4:8]) != 3 {
		return manifest, errors.New("invalid CRX3 package")
	}
	headerSize := uint64(binary.LittleEndian.Uint32(data[8:12]))
	if headerSize > 1<<20 || headerSize+12 >= uint64(len(data)) {
		return manifest, errors.New("invalid CRX3 header")
	}
	offset := int(headerSize + 12)
	fields, err := parseCRXFields(data[12:offset])
	if err != nil {
		return manifest, err
	}
	var signed []byte
	for _, field := range fields {
		if field.number == 10000 {
			if signed != nil {
				return manifest, errors.New("duplicate CRX3 signed header")
			}
			signed = field.data
		}
	}
	signedFields, err := parseCRXFields(signed)
	var extensionKey []byte
	for _, field := range signedFields {
		if field.number == 1 {
			if extensionKey != nil {
				return manifest, errors.New("duplicate CRX3 extension ID")
			}
			extensionKey = field.data
		}
	}
	if err != nil || len(extensionKey) != 16 || extensionID(extensionKey) != expectedID {
		return manifest, errors.New("CRX3 extension ID mismatch")
	}
	hash := sha256.New()
	hash.Write([]byte("CRX3 SignedData\x00"))
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(signed)))
	hash.Write(size[:])
	hash.Write(signed)
	hash.Write(data[offset:])
	digest := hash.Sum(nil)
	matched := false
	for _, field := range fields {
		if field.number != 2 && field.number != 3 {
			continue
		}
		proof, err := parseCRXFields(field.data)
		var publicKey, signature []byte
		for _, item := range proof {
			switch item.number {
			case 1:
				if publicKey != nil {
					return manifest, errors.New("duplicate CRX3 proof public key")
				}
				publicKey = item.data
			case 2:
				if signature != nil {
					return manifest, errors.New("duplicate CRX3 proof signature")
				}
				signature = item.data
			}
		}
		if err != nil || len(publicKey) == 0 || len(signature) == 0 {
			return manifest, errors.New("invalid CRX3 proof")
		}
		key, err := x509.ParsePKIXPublicKey(publicKey)
		if err != nil {
			return manifest, errors.New("invalid CRX3 public key")
		}
		valid := false
		switch pub := key.(type) {
		case *rsa.PublicKey:
			valid = field.number == 2 && rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, signature) == nil
		case *ecdsa.PublicKey:
			valid = field.number == 3 && ecdsa.VerifyASN1(pub, digest, signature)
		}
		if !valid {
			return manifest, errors.New("CRX3 signature verification failed")
		}
		keyHash := sha256.Sum256(publicKey)
		matched = matched || extensionID(keyHash[:16]) == expectedID
	}
	if !matched {
		return manifest, errors.New("CRX3 matching developer proof is missing")
	}
	archive, err := zip.NewReader(bytes.NewReader(data[offset:]), int64(len(data)-offset))
	if err != nil || len(archive.File) > 512 {
		return manifest, errors.New("invalid extension ZIP")
	}
	var total uint64
	seen := make(map[string]bool)
	for _, entry := range archive.File {
		name := entry.Name
		if name == "" || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != strings.TrimSuffix(name, "/") || seen[name] || entry.UncompressedSize64 > 32<<20 || entry.UncompressedSize64 > 64<<20-total || entry.Mode()&os.ModeSymlink != 0 {
			return manifest, errors.New("unsafe extension ZIP entry")
		}
		total += entry.UncompressedSize64
		seen[name] = true
		if name != "manifest.json" {
			continue
		}
		if entry.UncompressedSize64 > 1<<20 {
			return manifest, errors.New("extension manifest is too large")
		}
		reader, err := entry.Open()
		if err != nil {
			return manifest, err
		}
		payload, readErr := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || len(payload) > 1<<20 || json.Unmarshal(payload, &manifest) != nil {
			return manifest, errors.New("invalid extension manifest")
		}
	}
	if !seen["manifest.json"] || manifest.ManifestVersion != 3 || manifest.Name == "" || compareExtensionVersions(manifest.Version, manifest.Version) != 0 {
		return manifest, errors.New("invalid Manifest V3 extension")
	}
	return manifest, nil
}

func extensionID(data []byte) string {
	var text strings.Builder
	for _, b := range data {
		text.WriteByte('a' + b>>4)
		text.WriteByte('a' + b&15)
	}
	return text.String()
}

func parseCRXFields(data []byte) ([]crxField, error) {
	var fields []crxField
	for len(data) > 0 {
		tag, n := binary.Uvarint(data)
		if n <= 0 || tag>>3 == 0 || tag&7 != 2 {
			return nil, errors.New("invalid CRX3 protobuf")
		}
		data = data[n:]
		length, n := binary.Uvarint(data)
		if n <= 0 || length > uint64(len(data)-n) {
			return nil, errors.New("truncated CRX3 protobuf")
		}
		data = data[n:]
		fields = append(fields, crxField{tag >> 3, data[:int(length)]})
		data = data[int(length):]
	}
	return fields, nil
}

func normalizedExtensionVersion(value string) ([4]uint64, error) {
	var version [4]uint64
	parts := strings.Split(value, ".")
	if len(parts) < 1 || len(parts) > 4 {
		return version, errors.New("invalid extension version")
	}
	for i, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" || len(part) > 5 || (len(part) > 1 && part[0] == '0') {
			return version, errors.New("invalid extension version")
		}
		n, err := strconv.ParseUint(part, 10, 16)
		if err != nil {
			return version, err
		}
		version[i] = n
	}
	if version == [4]uint64{} {
		return version, errors.New("invalid extension version")
	}
	return version, nil
}

// -2 means invalid; trailing zero components have the same numeric version.
func compareExtensionVersions(a, b string) int {
	x, err := normalizedExtensionVersion(a)
	if err != nil {
		return -2
	}
	y, err := normalizedExtensionVersion(b)
	if err != nil {
		return -2
	}
	for i := range x {
		if x[i] < y[i] {
			return -1
		}
		if x[i] > y[i] {
			return 1
		}
	}
	return 0
}

func extensionSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validExtensionID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "abcdefghijklmnop") == ""
}

func extensionPackageError(err error) error {
	return fmt.Errorf("extension package validation: %w", err)
}
