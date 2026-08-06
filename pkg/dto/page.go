package dto

const (
	DefaultPageNumber = 1
	DefaultPageSize   = 10
	MaxPageSize       = 200
)

type Page struct {
	PageNumber int    `json:"pageNumber"`
	PageSize   int    `json:"pageSize"`
	TotalCount *int64 `json:"totalCount,omitempty"`
	OrderName  string `json:"orderName,omitempty"`
	OrderType  string `json:"orderType,omitempty"`
}

func (p *Page) Normalize() {
	if p.PageNumber <= 0 {
		p.PageNumber = DefaultPageNumber
	}
	if p.PageSize <= 0 {
		p.PageSize = DefaultPageSize
	}
	if p.PageSize > MaxPageSize {
		p.PageSize = MaxPageSize
	}
}

func (p *Page) Offset() int {
	p.Normalize()
	return (p.PageNumber - 1) * p.PageSize
}

func (p *Page) Limit() int {
	p.Normalize()
	return p.PageSize
}
