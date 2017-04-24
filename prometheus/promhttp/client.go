// Copyright 2016 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Copyright (c) 2013, The Prometheus Authors
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be found
// in the LICENSE file.

package promhttp

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// RoundTripperFunc is an adapter to allow wrapping an http.Client or other
// Middleware funcs, allowing the user to construct layers of middleware around
// an http client request.
type RoundTripperFunc func(req *http.Request) (*http.Response, error)

// RoundTrip implements the RoundTripper interface.
func (rt RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return rt(r)
}

// InstrumentTrace is used to offer flexibility in instrumenting the available
// httptrace.ClientTrace hooks. Each function is passed a float64 representing
// the time in seconds since the start of the http request. A user may choose
// to use separately buckets Histograms, or implement custom instance labels
// per function.
type InstrumentTrace struct {
	GotConn, PutIdleConn, GotFirstResponseByte, Got100Continue, DNSStart, DNSDone, ConnectStart, ConnectDone, TLSHandshakeStart, TLSHandshakeDone, WroteHeaders, Wait100Continue, WroteRequest func(float64)
}

// InstrumentRoundTripperTrace accepts an InstrumentTrace structand a
// http.RoundTripper, returning a RoundTripperFunc that wraps the supplied
// http.RoundTripper.
// Note: Partitioning histograms is expensive.
func InstrumentRoundTripperTrace(it *InstrumentTrace, next http.RoundTripper) RoundTripperFunc {
	return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var (
			start = time.Now()
		)

		trace := &httptrace.ClientTrace{
			GotConn: func(_ httptrace.GotConnInfo) {
				if it.GotConn != nil {
					it.GotConn(time.Since(start).Seconds())
				}
			},
			PutIdleConn: func(err error) {
				if err != nil {
					return
				}
				if it.PutIdleConn != nil {
					it.PutIdleConn(time.Since(start).Seconds())
				}
			},
			DNSStart: func(_ httptrace.DNSStartInfo) {
				if it.DNSStart != nil {
					it.DNSStart(time.Since(start).Seconds())
				}
			},
			DNSDone: func(_ httptrace.DNSDoneInfo) {
				if it.DNSStart != nil {
					it.DNSStart(time.Since(start).Seconds())
				}
			},
			ConnectStart: func(_, _ string) {
				if it.ConnectStart != nil {
					it.ConnectStart(time.Since(start).Seconds())
				}
			},
			ConnectDone: func(_, _ string, err error) {
				if err != nil {
					return
				}
				if it.ConnectDone != nil {
					it.ConnectDone(time.Since(start).Seconds())
				}
			},
			GotFirstResponseByte: func() {
				if it.GotFirstResponseByte != nil {
					it.GotFirstResponseByte(time.Since(start).Seconds())
				}
			},
			Got100Continue: func() {
				if it.Got100Continue != nil {
					it.Got100Continue(time.Since(start).Seconds())
				}
			},
			TLSHandshakeStart: func() {
				if it.TLSHandshakeStart != nil {
					it.TLSHandshakeStart(time.Since(start).Seconds())
				}
			},
			TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
				if err != nil {
					return
				}
				if it.TLSHandshakeDone != nil {
					it.TLSHandshakeDone(time.Since(start).Seconds())
				}
			},
			WroteHeaders: func() {
				if it.WroteHeaders != nil {
					it.WroteHeaders(time.Since(start).Seconds())
				}
			},
			Wait100Continue: func() {
				if it.Wait100Continue != nil {
					it.Wait100Continue(time.Since(start).Seconds())
				}
			},
			WroteRequest: func(_ httptrace.WroteRequestInfo) {
				if it.WroteRequest != nil {
					it.WroteRequest(time.Since(start).Seconds())
				}
			},
		}
		r = r.WithContext(httptrace.WithClientTrace(context.Background(), trace))

		return next.RoundTrip(r)
	})
}

// InstrumentRoundTripperInFlight accepts a Gauge and an http.RoundTripper,
// returning a new RoundTripperFunc that wraps the supplied http.RoundTripper.
// The provided Gauge must be registered in a registry in order to be used.
func InstrumentRoundTripperInFlight(gauge prometheus.Gauge, next http.RoundTripper) RoundTripperFunc {
	return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		gauge.Inc()
		resp, err := next.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		gauge.Dec()
		return resp, err
	})
}

// InstrumentRoundTripperCounter accepts an CounterVec interface and an
// http.RoundTripper, returning a new RoundTripperFunc that wraps the supplied
// http.RoundTripper. The provided CounterVec must be registered in a registry
// in order to be used.
func InstrumentRoundTripperCounter(counter *prometheus.CounterVec, next http.RoundTripper) RoundTripperFunc {
	code, method := checkLabels(counter)

	return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := next.RoundTrip(r)
		if err != nil {
			return nil, err
		}
		counter.With(labels(code, method, r.Method, resp.StatusCode)).Inc()
		return resp, err
	})
}

// InstrumentRoundTripperDuration accepts an ObserverVec interface and an
// http.RoundTripper, returning a new http.RoundTripper that wraps the supplied
// http.RoundTripper. The provided ObserverVec must be registered in a registry
// in order to be used. The instance labels "code" and "method" are supported
// on the provided ObserverVec. Note: Partitioning histograms is expensive.
func InstrumentRoundTripperDuration(obs prometheus.ObserverVec, next http.RoundTripper) RoundTripperFunc {
	code, method := checkLabels(obs)

	return RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		var (
			start     = time.Now()
			resp, err = next.RoundTrip(r)
		)
		if err != nil {
			return nil, err
		}
		obs.With(labels(code, method, r.Method, resp.StatusCode)).Observe(time.Since(start).Seconds())
		return resp, err
	})
}

func checkEventLabel(c prometheus.Collector) {
	var (
		desc *prometheus.Desc
		pm   dto.Metric
	)

	descc := make(chan *prometheus.Desc, 1)
	c.Describe(descc)

	select {
	case desc = <-descc:
	default:
		panic("no description provided by collector")
	}

	m, err := prometheus.NewConstMetric(desc, prometheus.UntypedValue, 0, "")
	if err != nil {
		panic("error checking metric for labels")
	}

	if err := m.Write(&pm); err != nil {
		panic("error checking metric for labels")
	}

	name := *pm.Label[0].Name
	if name != "event" {
		panic("metric partitioned with non-supported label")
	}
}
