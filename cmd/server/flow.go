package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The mesh: every institution at once. It is the DEPLOYMENT's read and the
// standing payment/recon has — it may open every institution's log precisely
// because no institution may — and it is why this is not on any node package.

// messageLog is what the mesh needs of an institution: its own half of every
// crossing. All three kinds keep one and no two of them are the same type.
type messageLog interface {
	ListMessages(ctx context.Context, f payment.MessageFilter) ([]payment.Message, error)
}

// NetworkFlow is every institution this deployment holds, the connections
// between them, and the files that crossed one. Limit bounds the crossings that
// have been DELIVERED; a file still resting on a wire is never paged out.
func (d *Deployment) NetworkFlow(ctx context.Context, limit int) (api.NetworkFlowDTO, error) {
	institutions, err := d.institutions(ctx)
	if err != nil {
		return api.NetworkFlowDTO{}, err
	}
	crossings, err := d.crossings(ctx)
	if err != nil {
		return api.NetworkFlowDTO{}, err
	}
	return api.NetworkFlowDTO{
		Institutions: institutions,
		Wires:        d.wires(),
		Crossings:    newestDelivered(crossings, limit),
	}, nil
}

// institutions is every node of the mesh: the two hosts this process
// configures, then every bank it holds a database for.
func (d *Deployment) institutions(ctx context.Context) ([]api.InstitutionDTO, error) {
	out := []api.InstitutionDTO{
		{BIC: string(d.cfg.CentralBankBIC), Name: centralBankName, Role: api.RoleSettlementAgent},
		{BIC: string(d.cfg.ClearingHouseBIC), Name: clearingHouseName, Role: api.RoleClearingHouse},
	}
	banks, err := d.Members(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range banks {
		out = append(out, api.InstitutionDTO{BIC: string(b.BIC), Name: b.Name, Role: api.RoleMemberBank})
	}
	return out, nil
}

// wires is who may dial whom: every member at both hosts, and the clearing
// house at the settlement agent. A bank the scheme has not admitted has none.
func (d *Deployment) wires() []api.WireDTO {
	out := []api.WireDTO{{Subscriber: string(d.cfg.ClearingHouseBIC), Host: string(d.cfg.CentralBankBIC)}}
	for _, b := range d.subscribers() {
		out = append(out,
			api.WireDTO{Subscriber: string(b.BIC()), Host: string(d.cfg.ClearingHouseBIC)},
			api.WireDTO{Subscriber: string(b.BIC()), Host: string(d.cfg.CentralBankBIC)})
	}
	return out
}

// messageLogs is every institution's message log, keyed by the institution
// holding it.
func (d *Deployment) messageLogs() map[iso20022.BIC]messageLog {
	out := map[iso20022.BIC]messageLog{
		d.cfg.ClearingHouseBIC: d.csm.Network(),
		d.cfg.CentralBankBIC:   d.cb.Network(),
	}
	for _, b := range d.banksInOrder() {
		out[b.BIC()] = b.Network()
	}
	return out
}

// crossingKey names one crossing: its two ends and the order id the host
// minted, which is what the two halves have in common. An order id is allocated
// by the host at one end, so it cannot collide with another pair's.
type crossingKey struct {
	from, to iso20022.BIC
	order    string
	// solo separates the crossings that carry no order id at all, which would
	// otherwise all merge into one per pair of ends.
	solo string
}

// crossings pairs every institution's own half of every crossing into the
// crossings themselves. A send with no take is a file resting on the wire; a
// take with no send is a record one end is missing.
func (d *Deployment) crossings(ctx context.Context) ([]api.CrossingDTO, error) {
	logs := d.messageLogs()
	// Every row, not a page of them: the delivered half of a crossing may be far
	// newer than the sent half, so a window per institution would report a
	// delivered file as resting on the wire.
	found := map[crossingKey]*api.CrossingDTO{}
	for _, holder := range slices.Sorted(maps.Keys(logs)) {
		messages, err := logs[holder].ListMessages(ctx, payment.MessageFilter{})
		if err != nil {
			return nil, fmt.Errorf("server: reading %s's message log for the mesh: %w", holder, err)
		}
		for _, m := range messages {
			key := keyOf(holder, m)
			c, held := found[key]
			if !held {
				c = &api.CrossingDTO{From: string(key.from), To: string(key.to), OrderID: key.order}
				found[key] = c
			}
			fill(c, m)
		}
	}

	out := make([]api.CrossingDTO, 0, len(found))
	for _, c := range found {
		out = append(out, *c)
	}
	slices.SortFunc(out, byWhenItCrossed)
	return out, nil
}

// keyOf names the crossing one logged message is a half of, read from the
// institution that logged it.
func keyOf(holder iso20022.BIC, m payment.Message) crossingKey {
	key := crossingKey{from: holder, to: m.Counterparty, order: m.OrderID}
	if m.Direction == payment.MessageReceived {
		key.from, key.to = m.Counterparty, holder
	}
	if m.OrderID == "" {
		key.solo = fmt.Sprintf("%s-%d", m.Direction, m.Seq)
	}
	return key
}

// fill records one end's half of a crossing. The header, the payments and the
// size are the sender's where it holds a record, because those are what it put
// on the wire.
func fill(c *api.CrossingDTO, m payment.Message) {
	at := m.At
	if m.Direction == payment.MessageReceived {
		c.ReceivedSeq, c.ReceivedAt = m.Seq, &at
		if c.SentSeq != 0 {
			return
		}
	} else {
		c.SentSeq, c.SentAt = m.Seq, &at
	}
	c.MsgDefIdr, c.MsgID, c.PayloadSize = m.MsgDefIdr, m.MsgID, len(m.Payload)
	c.Payments = make([]string, len(m.Payments))
	for i, id := range m.Payments {
		c.Payments[i] = string(id)
	}
}

// byWhenItCrossed orders the mesh by when each file LEFT, a crossing spanning
// an interval rather than an instant. Within one institution its own seq orders
// its traffic; between two the business date is all there is.
func byWhenItCrossed(a, b api.CrossingDTO) int {
	if c := crossedAt(a).Compare(crossedAt(b)); c != 0 {
		return c
	}
	if c := strings.Compare(a.From, b.From); c != 0 {
		return c
	}
	if c := cmp.Compare(a.SentSeq, b.SentSeq); c != 0 {
		return c
	}
	return strings.Compare(a.To+a.OrderID, b.To+b.OrderID)
}

func crossedAt(c api.CrossingDTO) time.Time {
	if c.SentAt != nil {
		return *c.SentAt
	}
	if c.ReceivedAt != nil {
		return *c.ReceivedAt
	}
	return time.Time{}
}

// newestDelivered keeps every crossing still resting on a wire and the newest
// limit of the ones that arrived. A queue nobody has come for is what this view
// exists to show, so it is never the thing a page drops.
func newestDelivered(crossings []api.CrossingDTO, limit int) []api.CrossingDTO {
	if limit <= 0 {
		return crossings
	}
	delivered := 0
	for _, c := range crossings {
		if c.ReceivedAt != nil {
			delivered++
		}
	}
	drop := delivered - limit
	out := make([]api.CrossingDTO, 0, len(crossings))
	for _, c := range crossings {
		if c.ReceivedAt != nil && drop > 0 {
			drop--
			continue
		}
		out = append(out, c)
	}
	return out
}
