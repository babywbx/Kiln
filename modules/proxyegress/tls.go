package proxyegress

import (
	"crypto/tls"
	"sync/atomic"
)

// Go 1.22 dropped these from the defaults; some CDN edges still offer nothing else.
var rsaKeyExchangeSuites = []uint16{
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
}

var upstreamRSAKeyExchange atomic.Bool

func init() {
	upstreamRSAKeyExchange.Store(true)
}

func SetUpstreamRSAKeyExchange(enabled bool) {
	upstreamRSAKeyExchange.Store(enabled)
}

func UpstreamTLSConfig() *tls.Config {
	return upstreamTLSConfig(upstreamRSAKeyExchange.Load())
}

func upstreamTLSConfig(rsaKeyExchange bool) *tls.Config {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if !rsaKeyExchange {
		return config
	}
	modern := tls.CipherSuites()
	suites := make([]uint16, 0, len(modern)+len(rsaKeyExchangeSuites))
	for _, suite := range modern {
		suites = append(suites, suite.ID)
	}
	config.CipherSuites = append(suites, rsaKeyExchangeSuites...)
	return config
}
