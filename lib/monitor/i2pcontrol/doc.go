// Package i2pcontrol implements a minimal client for the I2PControl
// JSON-RPC 2.0 interface exposed by I2P routers (Java I2P, i2pd, go-i2p).
//
// It is deliberately dependency-free (standard library only) and is used by
// the testnet dashboard to poll router status. Authentication follows the
// I2PControl spec: the client calls Authenticate, caches the returned token,
// and includes it in subsequent requests. When a request fails with an
// authentication error (bad/expired token) the client re-authenticates once
// and retries.
//
// TLS: i2pd serves I2PControl over HTTPS with a self-signed certificate it
// generates on first start. Chain verification can never succeed for such a
// cert, so this client supports explicit certificate pinning: the caller
// supplies the router's own i2pcontrol.crt.pem and the server is trusted only
// if it presents exactly that certificate. Verification is never disabled
// silently; WithInsecureTLS must be set explicitly by the operator.
package i2pcontrol
