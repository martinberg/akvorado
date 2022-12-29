// SPDX-FileCopyrightText: 2022 Tchadel Icard
// SPDX-License-Identifier: AGPL-3.0-only

// Package sflow handles sFlow v5 decoding.
package sflow

import (
	"bytes"
	"net"

	"github.com/netsampler/goflow2/decoders/sflow"
	"github.com/netsampler/goflow2/producer"

	"akvorado/common/reporter"
	"akvorado/inlet/flow/decoder"
)

const (
	// interfaceLocal is used for InIf and OutIf when the traffic is
	// locally originated or terminated. We need to translate it to 0.
	interfaceLocal = 0x3fffffff
	// interfaceOutMask is the mask to interpret output interface type
	interfaceOutMask = 0xc0000000
	// interfaceOutDiscard is used for OutIf when the traffic is discarded
	interfaceOutDiscard = 0x40000000
	// interfaceOutMultiple is used when there are multiple output interfaces
	interfaceOutMultiple = 0x80000000
)

// Decoder contains the state for the sFlow v5 decoder.
type Decoder struct {
	r *reporter.Reporter

	metrics struct {
		errors                *reporter.CounterVec
		stats                 *reporter.CounterVec
		sampleRecordsStatsSum *reporter.CounterVec
		sampleStatsSum        *reporter.CounterVec
	}
}

// New instantiates a new sFlow decoder.
func New(r *reporter.Reporter) decoder.Decoder {
	nd := &Decoder{
		r: r,
	}

	nd.metrics.errors = nd.r.CounterVec(
		reporter.CounterOpts{
			Name: "errors_count",
			Help: "sFlows processed errors.",
		},
		[]string{"exporter", "error"},
	)
	nd.metrics.stats = nd.r.CounterVec(
		reporter.CounterOpts{
			Name: "count",
			Help: "sFlows processed.",
		},
		[]string{"exporter", "agent", "version"},
	)
	nd.metrics.sampleRecordsStatsSum = nd.r.CounterVec(
		reporter.CounterOpts{
			Name: "sample_records_sum",
			Help: "sFlows samples sum of records.",
		},
		[]string{"exporter", "agent", "version", "type"},
	)
	nd.metrics.sampleStatsSum = nd.r.CounterVec(
		reporter.CounterOpts{
			Name: "sample_sum",
			Help: "sFlows samples sum.",
		},
		[]string{"exporter", "agent", "version", "type"},
	)

	return nd
}

// Decode decodes an sFlow payload.
func (nd *Decoder) Decode(in decoder.RawFlow) []*decoder.FlowMessage {
	buf := bytes.NewBuffer(in.Payload)
	key := in.Source.String()

	ts := uint64(in.TimeReceived.UTC().Unix())
	msgDec, err := sflow.DecodeMessage(buf)

	if err != nil {
		switch err.(type) {
		case *sflow.ErrorVersion:
			nd.metrics.errors.WithLabelValues(key, "error version").Inc()
		case *sflow.ErrorIPVersion:
			nd.metrics.errors.WithLabelValues(key, "error ip version").Inc()
		case *sflow.ErrorDataFormat:
			nd.metrics.errors.WithLabelValues(key, "error data format").Inc()
		default:
			nd.metrics.errors.WithLabelValues(key, "error decoding").Inc()
		}
		return nil
	}

	// Update some stats
	msgDecConv, ok := msgDec.(sflow.Packet)
	if !ok {
		nd.metrics.stats.WithLabelValues(key, "unknown", "unknwon").Inc()
		return nil
	}
	agent := net.IP(msgDecConv.AgentIP).String()
	version := "5"
	samples := msgDecConv.Samples
	nd.metrics.stats.WithLabelValues(key, agent, version).Inc()
	hasFlowSamples := false
	hasExpandedFlowSamples := false
	for _, s := range samples {
		switch sConv := s.(type) {
		case sflow.FlowSample:
			nd.metrics.sampleStatsSum.WithLabelValues(key, agent, version, "FlowSample").
				Inc()
			nd.metrics.sampleRecordsStatsSum.WithLabelValues(key, agent, version, "FlowSample").
				Add(float64(len(sConv.Records)))
			hasFlowSamples = true
		case sflow.ExpandedFlowSample:
			nd.metrics.sampleStatsSum.WithLabelValues(key, agent, version, "ExpandedFlowSample").
				Inc()
			nd.metrics.sampleRecordsStatsSum.WithLabelValues(key, agent, version, "ExpandedFlowSample").
				Add(float64(len(sConv.Records)))
			hasExpandedFlowSamples = true
		case sflow.CounterSample:
			nd.metrics.sampleStatsSum.WithLabelValues(key, agent, version, "CounterSample").
				Inc()
			nd.metrics.sampleRecordsStatsSum.WithLabelValues(key, agent, version, "CounterSample").
				Add(float64(len(sConv.Records)))
		}
	}
	if hasFlowSamples && hasExpandedFlowSamples {
		// We assume routers are either exporting one or the others. The
		// alternative would be to keep count of the received flows and their
		// types into a bitset. However, this would rely on the fact that
		// GoFlow2 keep everything in order and therefore may not be
		// future-proof. Better have people not have flows at all than having
		// something wrong.
		nd.metrics.errors.WithLabelValues(key, "sflow packet has both regular and expanded flows").Inc()
		return nil
	}

	flowMessageSet, _ := producer.ProcessMessageSFlow(msgDec)
	for _, fmsg := range flowMessageSet {
		fmsg.TimeReceived = ts
		fmsg.TimeFlowStart = ts
		fmsg.TimeFlowEnd = ts
	}

	results := make([]*decoder.FlowMessage, len(flowMessageSet))
	for idx, fmsg := range flowMessageSet {
		results[idx] = decoder.ConvertGoflowToFlowMessage(fmsg)
		if fmsg.InIf == interfaceLocal {
			results[idx].InIf = 0
		}
		if fmsg.OutIf == interfaceLocal {
			results[idx].OutIf = 0
		} else if hasFlowSamples && fmsg.OutIf&interfaceOutMask == interfaceOutDiscard {
			results[idx].OutIf = 0
			results[idx].ForwardingStatus = 128
		} else if hasFlowSamples && fmsg.OutIf&interfaceOutMask == interfaceOutMultiple {
			results[idx].OutIf = 0
		}
	}

	return results
}

// Name returns the name of the decoder.
func (nd *Decoder) Name() string {
	return "sflow"
}
