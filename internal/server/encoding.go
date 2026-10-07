package server

import "encoding/base64"

func SafeBase64Encode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
