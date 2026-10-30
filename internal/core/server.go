// Package core runs the independently owned demonstration core process.
package core

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/profile"
	"github.com/vitalcc55/gk80-control/internal/storage"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

// Unconfirmed operations retain their profile ownership until the process exits.
var retainedCoreResources struct {
	sync.Mutex
	items []struct {
		lock  *profile.Lock
		store *storage.Store
	}
}

// Run serves the core until the owner pipe closes or the server stops.
func Run(ctx context.Context, input io.Reader, output io.Writer, fallback io.Writer) (runErr error) {
	reader := bufio.NewReaderSize(input, 8193)
	var bootstrap transport.Bootstrap
	if err := transport.ReadFrame(reader, &bootstrap); err != nil {
		return err
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(bootstrap.Secret)
	if err != nil || len(secretBytes) != 32 || !filepath.IsAbs(bootstrap.ProfileDir) {
		return errors.New("invalid core configuration")
	}
	lock, err := profile.Acquire(bootstrap.ProfileDir)
	if err != nil {
		if errors.Is(err, profile.ErrInUse) {
			_ = json.NewEncoder(output).Encode(transport.Ready{ErrorCode: "profile_in_use"})
		}
		return errors.New("demo profile unavailable")
	}
	retainResources := false
	defer func() {
		if !retainResources {
			_ = lock.Close()
		}
	}()
	store, err := storage.Open(bootstrap.ProfileDir)
	if err != nil {
		return errors.New("demo storage unavailable")
	}
	defer func() {
		if !retainResources {
			_ = store.Close()
		}
	}()
	if err := store.Recover(ctx); err != nil {
		return errors.New("demo storage recovery unavailable")
	}
	runID, err := uuid.NewRandom()
	if err != nil {
		return errors.New("core identity unavailable")
	}
	logger, logErr := diagnostics.Open(filepath.Join(bootstrap.ProfileDir, "logs"), diagnostics.RoleCore, runID, fallback, diagnostics.Config{AppVersion: bootstrap.AppVersion, ProfileID: bootstrap.ProfileID})
	if logger == nil {
		return errors.New("invalid diagnostic configuration")
	}
	_ = logErr // Diagnostic failure is observable in logger status and must not block core.
	defer func() {
		logger.Application(diagnostics.ComponentCore).Info(string(diagnostics.ProcessStopped))
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := logger.Close(closeContext); err != nil {
			if runErr == nil {
				runErr = diagnostics.ErrCloseUnconfirmed
			} else {
				runErr = errors.Join(runErr, diagnostics.ErrCloseUnconfirmed)
			}
		}
	}()
	logger.Application(diagnostics.ComponentCore).Info(string(diagnostics.ProcessStarted))
	settings := store.Settings()
	logger.Application(diagnostics.ComponentCore).Info(string(diagnostics.StorageReady), "sqlite_version", settings.Version, "journal_mode", settings.JournalMode, "synchronous", settings.Synchronous)
	engine, err := demo.NewEngine(ctx, store, demo.NewSimulator(), runID)
	if err != nil {
		return errors.New("demo engine unavailable")
	}
	validator, err := contract.NewInputValidator()
	if err != nil {
		return errors.New("core contract unavailable")
	}
	leases := newUILeases(engine, 30*time.Second)
	feed := newEventFeed(engine)
	feedCtx, stopFeed := context.WithCancel(context.Background())
	feedDone := make(chan struct{})
	go func() { feed.run(feedCtx); close(feedDone) }()
	var server *http.Server
	var listener net.Listener
	var handler *coreHTTP
	defer func() {
		leases.close()
		stopFeed()
		var workersDone <-chan struct{}
		if handler != nil {
			workersDone = handler.stopWorkers()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		engineDone := make(chan error, 1)
		go func() { engineDone <- engine.Shutdown(shutdownCtx, "owner_lost") }()
		if server != nil {
			httpCtx, cancelHTTP := context.WithTimeout(shutdownCtx, 2*time.Second)
			if err := server.Shutdown(httpCtx); err != nil {
				retainResources = true
				_ = server.Close()
				runErr = errors.Join(runErr, errors.New("core HTTP shutdown unconfirmed"))
			}
			cancelHTTP()
		}
		if listener != nil {
			_ = listener.Close()
		}
		select {
		case err := <-engineDone:
			if err != nil {
				retainResources = true
				runErr = errors.Join(runErr, errors.New("core engine shutdown unconfirmed"))
			}
		case <-shutdownCtx.Done():
			retainResources = true
			runErr = errors.Join(runErr, errors.New("core engine shutdown unconfirmed"))
		}
		select {
		case <-feedDone:
		case <-shutdownCtx.Done():
			retainResources = true
			runErr = errors.Join(runErr, errors.New("core events shutdown unconfirmed"))
		}
		if workersDone != nil {
			select {
			case <-workersDone:
			case <-shutdownCtx.Done():
				retainResources = true
				runErr = errors.Join(runErr, errors.New("core requests shutdown unconfirmed"))
			}
		}
		if retainResources {
			retainedCoreResources.Lock()
			retainedCoreResources.items = append(retainedCoreResources.items, struct {
				lock  *profile.Lock
				store *storage.Store
			}{lock: lock, store: store})
			retainedCoreResources.Unlock()
		}
	}()
	certificate, fingerprint, err := ephemeralCertificate()
	if err != nil {
		return errors.New("core certificate unavailable")
	}
	listener, err = listen(ctx)
	if err != nil {
		return errors.New("core listener unavailable")
	}
	handler = &coreHTTP{secret: bootstrap.Secret, handshake: handshakeHandler(bootstrap.Secret, runID), validator: validator, leases: leases, feed: feed, engine: engine, store: store, logger: logger.Application(diagnostics.ComponentCore)}
	server = &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}},
	}
	ready := transport.Ready{Address: listener.Addr().String(), Fingerprint: hex.EncodeToString(fingerprint[:]), APIVersion: "v1", BackendRunID: runID, Demo: true}
	if err := json.NewEncoder(output).Encode(ready); err != nil {
		return errors.New("core bootstrap unavailable")
	}
	ownerDone := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, reader); close(ownerDone) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(tls.NewListener(listener, server.TLSConfig)) }()
	select {
	case <-ctx.Done():
	case <-ownerDone:
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("core listener stopped")
		}
	}
	return nil
}

func handshakeHandler(secret string, runID uuid.UUID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/handshake" {
			http.NotFound(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(contract.ApiError{Code: "unauthorized", Message: "Authentication required"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(contract.Handshake{ApiVersion: "v1", BackendRunId: runID, Demo: true})
	})
}

func ephemeralCertificate() (tls.Certificate, [32]byte, error) {
	var fingerprint [32]byte
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fingerprint, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fingerprint, err
	}
	now := time.Now()
	template := x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fingerprint, err
	}
	fingerprint = sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, fingerprint, nil
}
