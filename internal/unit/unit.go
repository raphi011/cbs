// Package unit runs an act inside one unit of work and returns its value,
// which a func(context.Context, Tx) error closure cannot do on its own.
package unit

import "context"

// Of is a store's Update or View: the seam that opens one unit of work. It is
// named at the call site so that read-only and writing stay distinguishable.
type Of[T any] func(context.Context, func(context.Context, T) error) error

// Run returns fn's value from inside one unit of work. A unit that does not
// commit yields the zero value, because nothing it built was kept.
func Run[T, R any](ctx context.Context, of Of[T], fn func(context.Context, T) (R, error)) (R, error) {
	var out R
	if err := of(ctx, func(ctx context.Context, tx T) error {
		var err error
		out, err = fn(ctx, tx)
		return err
	}); err != nil {
		var zero R
		return zero, err
	}
	return out, nil
}

// Run2 is Run for an act that answers with two values.
func Run2[T, A, B any](ctx context.Context, of Of[T], fn func(context.Context, T) (A, B, error)) (A, B, error) {
	var (
		a A
		b B
	)
	if err := of(ctx, func(ctx context.Context, tx T) error {
		var err error
		a, b, err = fn(ctx, tx)
		return err
	}); err != nil {
		var (
			zeroA A
			zeroB B
		)
		return zeroA, zeroB, err
	}
	return a, b, nil
}
