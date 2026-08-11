package bank

import (
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/product"
)

// The product catalogue's HTTP surface.
//
// Products are book-scoped like every other entity here, so every route hangs
// off a participant: the same product ID at two banks is two products, because
// a catalogue belongs to a bank.
func (s *surface) registerProductRoutes(mux *api.Router) {
	mux.HandleFunc("POST /products", s.handleCreateProduct)
	mux.HandleFunc("GET /products", s.handleListProducts)
	mux.HandleFunc("GET /products/{prid}", s.handleGetProduct)
	mux.HandleFunc("POST /products/{prid}/retire", s.handleRetireProduct)

	mux.HandleFunc("POST /products/{prid}/versions", s.handleDraftVersion)
	mux.HandleFunc("GET /products/{prid}/versions", s.handleListVersions)
	// The effective day is in the PATH rather than in a body, because that day
	// is the version's identity: a body carrying it would let a client publish
	// a different version from the one the URL names.
	mux.HandleFunc("POST /products/{prid}/versions/{day}/publish", s.handlePublishVersion)
}

func (s *surface) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.CreateProductRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	kind, err := api.KindFromString(req.Kind)
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	created, err := p.Catalogue.CreateProduct(r.Context(), req.Name, kind)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToProductDTO(created))
}

// handleListProducts returns the whole catalogue, RETIRED ENTRIES INCLUDED: a
// withdrawn product is still the product some accounts are on, and a client
// rendering one of those accounts has to be able to name it. Filtering the
// list a form offers is the client's job.
func (s *surface) handleListProducts(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	list, err := p.Catalogue.ListProducts(r.Context())
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.ProductDTO, len(list))
	for i, prd := range list {
		out[i] = api.ToProductDTO(prd)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

func (s *surface) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	got, err := p.Catalogue.GetProduct(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToProductDTO(got))
}

func (s *surface) handleRetireProduct(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	retired, err := p.Catalogue.RetireProduct(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToProductDTO(retired))
}

func (s *surface) handleDraftVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	var req api.DraftVersionRequest
	if err := api.DecodeJSON(r, &req); err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	dc, err := api.DayCountFromString(req.DayCount)
	if err != nil {
		api.WriteBadRequest(w, err.Error())
		return
	}
	drafted, err := p.Catalogue.DraftVersion(r.Context(), product.ID(r.PathValue("prid")), req.EffectiveFrom,
		product.OverdraftPricing{
			Rate:           interest.Rate(req.Rate),
			UnarrangedRate: interest.Rate(req.UnarrangedRate),
			DayCount:       dc,
		})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, api.ToProductVersionDTO(drafted))
}

// handleListVersions returns a product's whole timeline, oldest first, DRAFTS
// INCLUDED. An operator view has to be able to see what is queued, and the
// `published` flag is what tells the two apart.
func (s *surface) handleListVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	rows, err := p.Catalogue.Versions(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	out := make([]api.ProductVersionDTO, len(rows))
	for i, v := range rows {
		out[i] = api.ToProductVersionDTO(v)
	}
	api.WriteJSON(w, http.StatusOK, out)
}

// handlePublishVersion freezes the draft for one effective day and stamps its
// content hash. From then on it prices every account bound to the product whose
// own row carries no overlay.
//
// Publication is FORWARD-ONLY: a version effective before today is refused with
// 422. It would move interest already charged on every account bound to the
// product at once, and the audit log would be the only control on it.
// Retroactive repricing stays with the per-account overlay, where the blast
// radius is one named customer.
func (s *surface) handlePublishVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.participant(w, r)
	if !ok {
		return
	}
	day, err := time.Parse("2006-01-02", r.PathValue("day"))
	if err != nil {
		api.WriteBadRequest(w, "effective day must be YYYY-MM-DD")
		return
	}
	published, err := p.Catalogue.PublishVersion(r.Context(), product.ID(r.PathValue("prid")), day)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ToProductVersionDTO(published))
}
