package server

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/keyauth"
	"github.com/joybiswas007/remote-wv-go/internal/widevine"
)

type input struct {
	PSSH      string `json:"pssh,omitempty"`
	Challenge string `json:"challenge,omitempty"`
	License   string `json:"license,omitempty"`
	Passkey   string `json:"passkey,omitempty"`
	SuperUser int    `json:"su,omitempty"`
	Sudoer    int    `json:"sudoer,omitempty"`
}

var (
	PsshNotFound = errors.New("pssh field can not be emtpy")
)

func errHandler(c *fiber.Ctx, err error) error {
	if err == keyauth.ErrMissingOrMalformedAPIKey {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": err.Error()})
}

func (s *FiberServer) RegisterFiberRoutes() {
	v1 := s.Group("/v1", keyauth.New(keyauth.Config{
		KeyLookup:    "key:passkey",
		Validator:    s.ValidateAPIKey,
		ErrorHandler: errHandler,
	}))

	v1.Post("/challenge", s.ChallengeHandler)
	v1.Post("/key", s.KeyHandler)
	v1.Post("/arsenal/key", s.ArsenalKeyHandler)

	su := s.Group("/su", keyauth.New(keyauth.Config{
		KeyLookup:    "key:passkey",
		Validator:    s.SUChecker,
		ErrorHandler: errHandler,
	}))

	su.Post("/passkey", s.AddSudoerHandler)
	su.Post("/revoke", s.RevokeSudoerHandler)
}

func (s *FiberServer) ChallengeHandler(c *fiber.Ctx) error {
	i := new(input)
	if err := c.BodyParser(i); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if i.PSSH == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": PsshNotFound,
		})
	}

	cdm, err := getCDM(i.PSSH)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	licenseRequest, err := cdm.GetLicenseRequest()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	resp := fiber.Map{
		"challenge": licenseRequest,
		"pssh":      i.PSSH,
	}
	return c.Status(fiber.StatusOK).JSON(resp)
}

func (s *FiberServer) KeyHandler(c *fiber.Ctx) error {
	i := new(input)
	if err := c.BodyParser(i); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	if i.Challenge == "" || i.License == "" || i.PSSH == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": "license or challange or pssh field can not be empty",
		})
	}
	cdm, err := getCDM(i.PSSH)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	licenseRequest, err := base64.StdEncoding.DecodeString(i.Challenge)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	decodedLicense, err := base64.StdEncoding.DecodeString(i.License)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	keys, err := cdm.GetLicenseKeys(licenseRequest, decodedLicense)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	decryptionKey := ""

	for _, key := range keys {
		if key.Type == widevine.License_KeyContainer_CONTENT {
			decryptionKey += hex.EncodeToString(key.ID) + ":" + hex.EncodeToString(key.Value)
		}
	}

	if err := s.DB.Insert(i.PSSH, decryptionKey); err != nil {
		log.Printf("%s", err.Error())
	}

	return c.Status(http.StatusOK).JSON(fiber.Map{
		"key":  decryptionKey,
		"pssh": i.PSSH,
	})
}

func (s *FiberServer) ArsenalKeyHandler(c *fiber.Ctx) error {
	i := new(input)
	if err := c.BodyParser(i); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	if i.PSSH == "" {
		return c.Status(http.StatusBadRequest).JSON(fiber.Map{
			"error": PsshNotFound,
		})
	}

	key, err := s.DB.Get(i.PSSH)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"key":  key.DecryptionKey,
		"pssh": key.PSSH,
	})
}

func (s *FiberServer) AddSudoerHandler(c *fiber.Ctx) error {
	i := new(input)
	if err := c.BodyParser(i); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	pk, err := generatePasskey()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	if err := s.DB.SudoSU(pk, i.SuperUser, i.Sudoer); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"passkey": pk,
		"message": "Save the passkey, without it you won't be able to request for keys",
	})

}

func (s *FiberServer) RevokeSudoerHandler(c *fiber.Ctx) error {
	i := new(input)
	if err := c.BodyParser(i); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": err.Error(),
		})
	}
	if i.Passkey == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "In order to revoke access, you need pass the passkey.",
		})
	}

	if err := s.DB.Revoke(i.Passkey); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": err.Error(),
		})
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"message": "access has been revoked",
	})
}

func getCDM(pssh string) (*widevine.CDM, error) {
	clientIDPath := os.Getenv("WV_CLIENT_ID")
	privateKeyPath := os.Getenv("WV_PRIVATE_KEY")

	if clientIDPath == "" || privateKeyPath == "" {
		return nil, errors.New("failed to load widevine client_id or private_key")
	}

	clientID, err := readAsByte(clientIDPath)
	if err != nil {
		return nil, err
	}

	privateKey, err := readAsByte(privateKeyPath)
	if err != nil {
		return nil, err
	}
	initData, err := base64.StdEncoding.DecodeString(pssh)
	if err != nil {
		return nil, err
	}
	cdm, err := widevine.NewCDM(string(privateKey), clientID, initData)
	if err != nil {
		return nil, err
	}
	return &cdm, nil
}

// readAsByte() read file as byte and return the byte
func readAsByte(filename string) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	bs := make([]byte, stat.Size())
	_, err = file.Read(bs)

	if err != nil {
		return nil, err
	}

	return bs, nil
}

// generatePasskey generates random 16 bytes base32 token
func generatePasskey() (string, error) {
	randomBytes := make([]byte, 16)
	_, err := rand.Read(randomBytes)
	if err != nil {
		return "", err
	}
	token := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes)
	return token, nil
}