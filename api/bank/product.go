package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// The product catalogue's HTTP surface.
//
// Products are book-scoped like every other entity here, so every route hangs
// off a participant: the same product ID at two banks is two products, because
// a catalogue belongs to a bank.
func (s *surface) registerProductRoutes(mux *api.Router) {
	mux.HandleFunc("POST /products", handleBody(s, http.StatusCreated, s.handleCreateProduct))
	mux.HandleFunc("GET /products", handle(s, http.StatusOK, s.handleListProducts))
	mux.HandleFunc("GET /products/{prid}", handle(s, http.StatusOK, s.handleGetProduct))
	mux.HandleFunc("POST /products/{prid}/retire", handle(s, http.StatusOK, s.handleRetireProduct))

	mux.HandleFunc("POST /products/{prid}/versions", handleBody(s, http.StatusCreated, s.handleDraftVersion))
	mux.HandleFunc("GET /products/{prid}/versions", handle(s, http.StatusOK, s.handleListVersions))
	// The effective day is in the PATH rather than in a body, because that day
	// is the version's identity: a body carrying it would let a client publish
	// a different version from the one the URL names.
	mux.HandleFunc("POST /products/{prid}/versions/{day}/publish", handle(s, http.StatusOK, s.handlePublishVersion))
}

func (s *surface) handleCreateProduct(r *http.Request, p *payment.Bank, req api.CreateProductRequest) (api.ProductDTO, error) {
	kind, err := api.KindFromString(req.Kind)
	if err != nil {
		return api.ProductDTO{}, err
	}
	created, err := p.Catalogue.CreateProduct(r.Context(), req.Name, kind)
	if err != nil {
		return api.ProductDTO{}, err
	}
	return api.ToProductDTO(created), nil
}

// handleListProducts returns the whole catalogue, RETIRED ENTRIES INCLUDED: a
// withdrawn product is still the product some accounts are on, and a client
// rendering one of those accounts has to be able to name it. Filtering the
// list a form offers is the client's job.
func (s *surface) handleListProducts(r *http.Request, p *payment.Bank) ([]api.ProductDTO, error) {
	list, err := p.Catalogue.ListProducts(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]api.ProductDTO, len(list))
	for i, prd := range list {
		out[i] = api.ToProductDTO(prd)
	}
	return out, nil
}

func (s *surface) handleGetProduct(r *http.Request, p *payment.Bank) (api.ProductDTO, error) {
	got, err := p.Catalogue.GetProduct(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		return api.ProductDTO{}, err
	}
	return api.ToProductDTO(got), nil
}

func (s *surface) handleRetireProduct(r *http.Request, p *payment.Bank) (api.ProductDTO, error) {
	retired, err := p.Catalogue.RetireProduct(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		return api.ProductDTO{}, err
	}
	return api.ToProductDTO(retired), nil
}

func (s *surface) handleDraftVersion(r *http.Request, p *payment.Bank, req api.DraftVersionRequest) (api.ProductVersionDTO, error) {
	dc, err := api.DayCountFromString(req.DayCount)
	if err != nil {
		return api.ProductVersionDTO{}, err
	}
	drafted, err := p.Catalogue.DraftVersion(r.Context(), product.ID(r.PathValue("prid")), req.EffectiveFrom,
		product.OverdraftPricing{
			Rate:           interest.Rate(req.Rate),
			UnarrangedRate: interest.Rate(req.UnarrangedRate),
			DayCount:       dc,
		})
	if err != nil {
		return api.ProductVersionDTO{}, err
	}
	return api.ToProductVersionDTO(drafted), nil
}

// handleListVersions returns a product's whole timeline, oldest first, DRAFTS
// INCLUDED. An operator view has to be able to see what is queued, and the
// `published` flag is what tells the two apart.
func (s *surface) handleListVersions(r *http.Request, p *payment.Bank) ([]api.ProductVersionDTO, error) {
	rows, err := p.Catalogue.Versions(r.Context(), product.ID(r.PathValue("prid")))
	if err != nil {
		return nil, err
	}
	out := make([]api.ProductVersionDTO, len(rows))
	for i, v := range rows {
		out[i] = api.ToProductVersionDTO(v)
	}
	return out, nil
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
func (s *surface) handlePublishVersion(r *http.Request, p *payment.Bank) (api.ProductVersionDTO, error) {
	day, err := api.ParseDay("day", r.PathValue("day"))
	if err != nil {
		return api.ProductVersionDTO{}, err
	}
	published, err := p.Catalogue.PublishVersion(r.Context(), product.ID(r.PathValue("prid")), day)
	if err != nil {
		return api.ProductVersionDTO{}, err
	}
	return api.ToProductVersionDTO(published), nil
}
