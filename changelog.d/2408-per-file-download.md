### Fixed
- **Download the file you picked, when a book has more than one** (#2408). A book holding several ebooks offered a single Download button that always served the same file, because the endpoint could only resolve one path per format. Every file in the list now has its own Download link, and the download endpoint takes `?path=` to name one. Audiobook folders still come down as a zip.
