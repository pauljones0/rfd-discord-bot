package models

// TitleRequest is a single item in a batch title-cleaning request.
type TitleRequest struct {
	Index    int
	Title    string
	Retailer string
	Price    string
}
