package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
)

const (
	scrapingURL     = "https://www.trustpilot.com/review/%s"
	scrapingPageURL = "https://www.trustpilot.com/review/%s?page=%d"
	productName     = "invideo.io"
)

type Review struct {
	Text   string `json:"text"`
	Date   string `json:"date"`
	Rating string `json:"rating"`
	Title  string `json:"title"`
	Link   string `json:"link"`
}

type ProductReviews struct {
	ProductName string    `json:"product_name"`
	Reviews     []*Review `json:"reviews"`
}

func main() {
	log.Printf("Start scraping reviews for %s", productName)

	productReviews, err := getProductReviews(productName)
	if err != nil {
		log.Fatal(err)
	}

	jsonFile, err := os.Create(fmt.Sprintf("trustpilot_reviews_%s.json", productName))
	if err != nil {
		log.Fatal(err)
	}
	defer jsonFile.Close()

	jsonEncoder := json.NewEncoder(jsonFile)
	err = jsonEncoder.Encode(productReviews)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Successfully scraped %d reviews for %s", len(productReviews.Reviews), productName)
}

func getProductReviews(name string) (*ProductReviews, error) {
	log.Printf("Start scraping page 1 for %s", name)

	productURL := fmt.Sprintf(scrapingURL, name)
	// make a request to the product page
	res, err := http.Get(productURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	// transform the HTML document into a goquery document which will allow us to use a jquery-like syntax
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}

	reviews := make([]*Review, 0)
	// we synchronize reviews processing with a channel, as we scrape reviews from multiple pages in parallel
	reviewsChan := make(chan *Review)
	quitChan := make(chan struct{})

	// we append reviews in a separate goroutine from reviewsChan
	go func() {
		for review := range reviewsChan {
			reviews = append(reviews, review)
		}

		close(quitChan)
	}()

	// to avoid one extra request, we process first page here separately
	doc.Find("div").Each(extractReviewFunc(reviewsChan, productURL))

	// we need to find a link to last page and extract the number of pages for the product
	doc.Find("a[name='pagination-button-last']").Each(extractReviewsOverPagesFunc(reviewsChan, name))

	close(reviewsChan)

	// wait until all reviews are appended
	<-quitChan

	return &ProductReviews{
		ProductName: name,
		Reviews:     reviews,
	}, nil
}

func extractReviewsOverPagesFunc(reviews chan<- *Review, name string) func(i int, s *goquery.Selection) {
	return func(i int, s *goquery.Selection) {
		href, exists := s.Attr("href")
		if !exists {
			return
		}

		// we need to find a link to pages and extract the number of pages for the product
		match, err := regexp.MatchString("page=\\d+", href)
		if err != nil || !match {
			return
		}

		re := regexp.MustCompile("\\d+")
		lastPage := re.FindString(href)
		lastPageInt, err := strconv.Atoi(lastPage)
		if err != nil {
			log.Printf("Cannot parse last page %s: %s\n", lastPage, err)

			return
		}

		// scrape all pages in parallel
		wg := &sync.WaitGroup{}
		for i := 2; i <= lastPageInt; i++ {
			wg.Add(1)
			go func(pageNumber int) {
				defer wg.Done()

				pageReviews, err := getPageProductReviews(name, pageNumber)
				if err != nil {
					log.Printf("Cannot get page %d product reviews: %s", pageNumber, err)

					return
				}

				for _, review := range pageReviews {
					reviews <- review
				}
			}(i)
		}

		wg.Wait()
	}
}

func getPageProductReviews(name string, page int) ([]*Review, error) {
	log.Printf("Start scraping page %d for %s", page, name)

	// productURL is used to construct a link to the review. It's pure, without query params
	productURL := fmt.Sprintf(scrapingURL, name)
	// actual request URL for scraping a page
	productRequestURL := fmt.Sprintf(scrapingPageURL, name, page)
	res, err := http.Get(productRequestURL)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		return nil, err
	}

	reviews := make([]*Review, 0)
	reviewsChan := make(chan *Review)
	quitChan := make(chan struct{})

	go func() {
		for review := range reviewsChan {
			reviews = append(reviews, review)
		}

		close(quitChan)
	}()

	// extract reviews from the page
	doc.Find("div").Each(extractReviewFunc(reviewsChan, productURL))

	close(reviewsChan)
	<-quitChan

	return reviews, nil
}

func extractReviewFunc(reviews chan<- *Review, productURL string) func(i int, s *goquery.Selection) {
	return func(i int, s *goquery.Selection) {
		classes, exists := s.Attr("class")
		if !exists {
			return
		}

		// validate if the div is a review card and a card wrapper (to avoid processing other divs, like advertisement)
		isReviewCard := false
		isCardWrapper := false

		for _, class := range strings.Split(classes, " ") {
			if strings.HasPrefix(class, "styles_reviewCard__") {
				isReviewCard = true
			}

			if strings.HasPrefix(class, "styles_cardWrapper__") {
				isCardWrapper = true
			}
		}

		if !isReviewCard || !isCardWrapper {
			return
		}

		// extract review data
		dateOfPost := s.Find("time").AttrOr("datetime", "")
		textOfReview := s.Find("p[data-service-review-text-typography]").Text()

		title := s.Find("h2").Text()
		link, _ := s.Find("a[data-review-title-typography]").Attr("href")
		if link != "" {
			link = productURL + link
		}

		// we don't transform the data in place, as we want to keep the original data for future analysis
		rating := s.Find("img").AttrOr("alt", "")

		reviews <- &Review{
			Text:   textOfReview,
			Date:   dateOfPost,
			Rating: rating,
			Title:  title,
			Link:   link,
		}
	}
}
