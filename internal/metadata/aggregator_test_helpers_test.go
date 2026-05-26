package metadata

import (
	"context"
	"time"

	"github.com/vavallee/bindery/internal/models"
)

type mockProvider struct {
	name                 string
	searchBooks          []models.Book
	searchBooksByQuery   map[string][]models.Book
	searchBookErr        error
	searchAuthors        []models.Author
	searchAuthorsByQuery map[string][]models.Author
	searchAuthErr        error
	searchAuthorQueries  []string
	getAuthor            *models.Author
	getAuthorErr         error
	getBook              *models.Book
	getBookByID          map[string]*models.Book
	getBookErr           error
	getBookCalls         int
	gotBookIDs           []string
	getEditions          []models.Edition
	getEditionsErr       error
	getByISBN            *models.Book
	getByISBNByISBN      map[string]*models.Book
	getByISBNErr         error
	getByISBNCalls       int
	gotISBNs             []string
	searchBookQueries    []string
	// authorWorks implements worksProvider interface
	authorWorks    []models.Book
	authorWorksErr error
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) SearchAuthors(_ context.Context, query string) ([]models.Author, error) {
	m.searchAuthorQueries = append(m.searchAuthorQueries, query)
	if m.searchAuthorsByQuery != nil {
		if authors, ok := m.searchAuthorsByQuery[query]; ok {
			return authors, m.searchAuthErr
		}
	}
	return m.searchAuthors, m.searchAuthErr
}
func (m *mockProvider) SearchBooks(_ context.Context, query string) ([]models.Book, error) {
	m.searchBookQueries = append(m.searchBookQueries, query)
	if m.searchBooksByQuery != nil {
		if books, ok := m.searchBooksByQuery[query]; ok {
			return books, m.searchBookErr
		}
	}
	return m.searchBooks, m.searchBookErr
}
func (m *mockProvider) GetAuthor(_ context.Context, _ string) (*models.Author, error) {
	return m.getAuthor, m.getAuthorErr
}
func (m *mockProvider) GetBook(_ context.Context, foreignID string) (*models.Book, error) {
	m.getBookCalls++
	m.gotBookIDs = append(m.gotBookIDs, foreignID)
	if m.getBookByID != nil {
		return m.getBookByID[foreignID], m.getBookErr
	}
	return m.getBook, m.getBookErr
}
func (m *mockProvider) GetEditions(_ context.Context, _ string) ([]models.Edition, error) {
	return m.getEditions, m.getEditionsErr
}
func (m *mockProvider) GetBookByISBN(_ context.Context, isbn string) (*models.Book, error) {
	m.getByISBNCalls++
	m.gotISBNs = append(m.gotISBNs, isbn)
	if m.getByISBNByISBN != nil {
		return m.getByISBNByISBN[isbn], m.getByISBNErr
	}
	return m.getByISBN, m.getByISBNErr
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalCanonicalQueries(a, b []primaryBookCanonicalQuery) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// worksProvider implementation (optional, only attached when needed).
type mockWorksProvider struct {
	mockProvider
	authorWorksCalls int
}

func (m *mockWorksProvider) GetAuthorWorks(_ context.Context, _ string) ([]models.Book, error) {
	m.authorWorksCalls++
	return m.authorWorks, m.authorWorksErr
}

type mockAuthorWorksByNameProvider struct {
	mockProvider
	authorWorksByName    []models.Book
	authorWorksByNameErr error
	gotAuthorName        string
	calls                int
}

func (m *mockAuthorWorksByNameProvider) GetAuthorWorksByName(_ context.Context, authorName string) ([]models.Book, error) {
	m.calls++
	m.gotAuthorName = authorName
	return m.authorWorksByName, m.authorWorksByNameErr
}

func newTestAggregator(primary Provider, enrichers ...Provider) *Aggregator {
	return &Aggregator{
		primary:   primary,
		enrichers: enrichers,
		cache:     newTTLCache(time.Minute),
	}
}
