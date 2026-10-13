package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/draw"
	"golang.org/x/image/webp"

	"github.com/alborghi-42/hypertube/backend/internal/repository"
)

const (
	avatarMaxBytes = 5 << 20 // uploaded file
	avatarMaxPixel = 8000    // per side, checked before decoding (decompression bombs)
	avatarSize     = 512     // stored images are downscaled to fit this square
	// avatarURLPrefix is relative to the app base, like every frontend API URL.
	avatarURLPrefix = "api/avatars/"
)

var avatarFilePattern = regexp.MustCompile(`^[0-9a-f]{32}\.(jpg|png)$`)

type avatarStore struct{ dir string }

func newAvatarStore(dataDir string) *avatarStore {
	return &avatarStore{dir: filepath.Join(dataDir, "avatars")}
}

var errBadImage = errors.New("unsupported or corrupt image")

// process sniffs the real type (never trusting the client's Content-Type or
// file name), decodes it and re-encodes a fresh JPEG or PNG. Re-encoding drops
// EXIF/metadata and anything appended to the file; GIFs keep their first frame.
func (s *avatarStore) process(data []byte) (encoded []byte, ext string, err error) {
	var decode func(io.Reader) (image.Image, error)
	var decodeConfig func(io.Reader) (image.Config, error)
	switch http.DetectContentType(data) {
	case "image/jpeg":
		decode, decodeConfig, ext = jpeg.Decode, jpeg.DecodeConfig, "jpg"
	case "image/png":
		decode, decodeConfig, ext = png.Decode, png.DecodeConfig, "png"
	case "image/gif":
		decode, decodeConfig, ext = gif.Decode, gif.DecodeConfig, "png"
	case "image/webp":
		decode, decodeConfig, ext = webp.Decode, webp.DecodeConfig, "png"
	default:
		return nil, "", errBadImage
	}
	cfg, err := decodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > avatarMaxPixel || cfg.Height > avatarMaxPixel {
		return nil, "", errBadImage
	}
	src, err := decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errBadImage
	}
	img := fit(src, avatarSize)

	var buf bytes.Buffer
	if ext == "jpg" {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88})
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return nil, "", err
	}
	return buf.Bytes(), ext, nil
}

// fit downscales src to fit a max x max box, keeping the aspect ratio. It
// always returns a new RGBA image so nothing from the source survives as-is.
func fit(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > maxSide || h > maxSide {
		if w >= h {
			w, h = maxSide, max(1, h*maxSide/b.Dx())
		} else {
			w, h = max(1, w*maxSide/b.Dy()), maxSide
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// save writes the image under a random name and returns that name.
func (s *avatarStore) save(data []byte, ext string) (string, error) {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	name := hex.EncodeToString(raw) + "." + ext
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.dir, name)); err != nil {
		return "", err
	}
	return name, nil
}

// remove deletes a stored avatar given its URL; external URLs (OAuth) are ignored.
func (s *avatarStore) remove(url *string) {
	if url == nil || !strings.HasPrefix(*url, avatarURLPrefix) {
		return
	}
	name := strings.TrimPrefix(*url, avatarURLPrefix)
	if avatarFilePattern.MatchString(name) {
		_ = os.Remove(filepath.Join(s.dir, name))
	}
}

// POST /api/users/me/avatar — multipart form, field "avatar".
func (h *ProfileHandler) uploadAvatar(c *gin.Context) {
	// Small allowance on top of the file for the multipart envelope.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, avatarMaxBytes+64<<10)
	file, header, err := c.Request.FormFile("avatar")
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Image must be at most 5 MB"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing image file"})
		return
	}
	defer file.Close()
	if header.Size > avatarMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Image must be at most 5 MB"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, avatarMaxBytes+1))
	if err != nil || len(data) > avatarMaxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Image must be at most 5 MB"})
		return
	}

	encoded, ext, err := h.avatars.process(data)
	if errors.Is(err, errBadImage) {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "Image must be a JPEG, PNG, WebP or GIF"})
		return
	}
	if err != nil {
		h.log.Error("encode avatar failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save image"})
		return
	}
	name, err := h.avatars.save(encoded, ext)
	if err != nil {
		h.log.Error("store avatar failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save image"})
		return
	}

	user := CurrentUser(c)
	url := avatarURLPrefix + name
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if err := h.q.UpdateUserAvatar(ctx, repository.UpdateUserAvatarParams{ID: user.ID, AvatarUrl: &url}); err != nil {
		h.avatars.remove(&url)
		h.log.Error("update avatar failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save image"})
		return
	}
	h.avatars.remove(user.AvatarUrl)
	updated := *user
	updated.AvatarUrl = &url
	c.JSON(http.StatusOK, selfView(&updated))
}

// GET /api/avatars/:file — public; names are random and never reused, so the
// response is cacheable forever.
func (h *ProfileHandler) serveAvatar(c *gin.Context) {
	name := c.Param("file")
	if !avatarFilePattern.MatchString(name) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}
	path := filepath.Join(h.avatars.dir, name)
	if _, err := os.Stat(path); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(path)
}
