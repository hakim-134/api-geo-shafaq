package places

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/hakim-134/api-geo-shafaq/internal/store"
)

var ErrNoPlace = errors.New("place not found")

// Face is a place's picture (P1111 §5): the parts of Frame drawn in one frame, the place
// filled. A stranger who has never seen Jondor's shape finds it in Buxoro's.
type Face struct {
	Place  *Place
	Frame  *Place // whose parts are drawn
	Whole  bool   // the place is the frame (the country): every part is it
	Shapes []store.PlaceMapShapesRow
}

// FaceOf draws a place's face. The frame is the parent, except:
//   - the country is its own frame, all of it filled;
//   - a city directly under the country that a region surrounds (Tashkent, in the hole of
//     Toshkent viloyati) is shown in that region, its own shape drawn into the hole. Shown in
//     the country it would be a dot; in its region it is a place.
func FaceOf(ctx context.Context, q *store.Queries, t *Tree, slug string) (*Face, error) {
	p := t.Get(slug)
	if p == nil {
		return nil, ErrNoPlace
	}
	f := &Face{Place: p, Frame: p.Parent}
	also := []string{}
	switch {
	case p.Parent == nil:
		f.Frame, f.Whole = p, true
		if len(p.Children) == 0 {
			also = append(also, p.Slug) // nothing inside to draw: the place alone
		}
	case p.Kind == "city" && p.Parent.Kind == "country":
		around, err := q.SurroundingPlace(ctx, p.Slug)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if r := t.Get(around); r != nil {
			f.Frame, also = r, append(also, p.Slug)
		}
	}
	frame := f.Frame.Slug
	if f.Whole && len(also) > 0 {
		frame = "" // only the place itself
	}
	rows, err := q.PlaceMapShapes(ctx, store.PlaceMapShapesParams{Slug: frame, Also: also})
	if err != nil {
		return nil, err
	}
	f.Shapes = rows
	return f, nil
}
