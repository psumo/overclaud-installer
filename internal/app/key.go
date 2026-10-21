package app

// The Overclaud release signing public key (Ed25519, hex). Releases are signed with
// cmd/overclaud-release on the maintainer's machine; the private key never ships.
func init() {
	if ReleasePublicKey == "" {
		ReleasePublicKey = "9be08e46ce78c2689cf35accff90ef5c2acbc2620738c83fe4ad798a9d0db56b"
	}
}
