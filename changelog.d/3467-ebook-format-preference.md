### Fixed
- **A book with several ebook formats now serves the EPUB first** (#3467). When a release held, say, AZW3, EPUB and MOBI, the book's main file was whichever was imported first, so OPDS and downloads handed out the AZW3. The main file now follows one order: EPUB, KEPUB, AZW3, MOBI, PDF, then anything else. Every file is still kept. Thanks to kennethp1015 for the report.
