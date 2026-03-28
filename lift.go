package reflow

import "context"

// Lift wraps a plain function as an Act closure. The envelope's metadata
// is carried through automatically — you just write the transform.
//
//	parse := &reflow.Node[string, JSON]{
//	    Name: "parse",
//	    Act:  reflow.Lift(func(raw string) (JSON, error) {
//	        var v JSON
//	        return v, json.Unmarshal([]byte(raw), &v)
//	    }),
//	}
func Lift[I, O any](fn func(I) (O, error)) func(context.Context, Envelope[I]) (Envelope[O], error) {
	return func(_ context.Context, in Envelope[I]) (Envelope[O], error) {
		v, err := fn(in.Value)
		return Envelope[O]{Value: v, Meta: in.Meta}, err
	}
}

// LiftCtx is like Lift but the function receives a context.
func LiftCtx[I, O any](fn func(context.Context, I) (O, error)) func(context.Context, Envelope[I]) (Envelope[O], error) {
	return func(ctx context.Context, in Envelope[I]) (Envelope[O], error) {
		v, err := fn(ctx, in.Value)
		return Envelope[O]{Value: v, Meta: in.Meta}, err
	}
}

// Pass wraps a function that transforms a value without error as an Act.
// Useful for simple mappings.
//
//	double := &reflow.Node[int, int]{
//	    Name: "double",
//	    Act:  reflow.Pass(func(n int) int { return n * 2 }),
//	}
func Pass[I, O any](fn func(I) O) func(context.Context, Envelope[I]) (Envelope[O], error) {
	return func(_ context.Context, in Envelope[I]) (Envelope[O], error) {
		return Envelope[O]{Value: fn(in.Value), Meta: in.Meta}, nil
	}
}
