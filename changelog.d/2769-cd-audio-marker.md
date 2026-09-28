### Fixed
- **Print editions no longer mistaken for audiobooks over a stray "cd"** (#2769): Hardcover edition text was checked for "cd" anywhere, so a name like McDermott could mark a print edition as audio, give the book an ASIN it should not have and widen its media type. "cd" now has to be a word on its own, so Audio CD, 2 CDs and CD-ROM still count. Thanks to @mavonx for the fix.
