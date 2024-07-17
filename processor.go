package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/davidbyttow/govips/v2/vips"
	"io/fs"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type ImageData struct {
	FullPath    string `json:"full_path"`
	ThumbPath   string `json:"thumb_path"`
	DisplayPath string `json:"display_path"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Tiles       string `json:"tiles,omitempty"`
	MaxWidth    int    `json:"max_width,omitempty"`
	MaxHeight   int    `json:"max_height,omitempty"`
	path        string `json:"-"`
	name        string `json:"-"`
}

var logger = log.Default()

const thumbnailHeight = 400
const slideHeight = 2000
const tileMinDimension = 4100

func main() {
	flag.Parse()
	if len(flag.Args()) != 1 {
		panic("Must provide a directory")
	}
	root := flag.Args()[0]

	var imageDataMap = map[string]map[string]*ImageData{}
	var results = make(chan *ImageData, 100)

	logger.Printf("Building image file list...")

	images, errc := buildImageList(root)

	vips.Startup(nil)
	vips.LoggingSettings(nil, vips.LogLevelMessage)
	defer vips.Shutdown()

	var wg sync.WaitGroup
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		wg.Add(1)
		go func() {
			processor(i, images, results)
			wg.Done()
		}()
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		resultDir := filepath.Dir(result.path)
		if _, exists := imageDataMap[resultDir]; !exists {
			imageDataMap[resultDir] = map[string]*ImageData{}
		}
		imageDataMap[resultDir][result.name] = result
	}

	for dir, imageData := range imageDataMap {
		go writeDirImageData(dir, imageData)
	}

	if err := <-errc; err != nil {
		logger.Fatal(err)
	}
}

func buildImageList(root string) (<-chan *ImageData, <-chan error) {
	skipFileNames := []string{".DS_Store", "thumbnail", "display", "html", "dzi", "json", "xml"}
	images := make(chan *ImageData, 100)
	errc := make(chan error, 1)

	go func() {
		defer close(images)
		errc <- filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			for _, skipFileName := range skipFileNames {
				// don't process non-images or already generated images
				if strings.Contains(d.Name(), skipFileName) {
					return nil
				}
			}

			if d.IsDir() {
				// skip dz tiles generated externally or previously
				if strings.HasSuffix(d.Name(), "_files") {
					return filepath.SkipDir
				}
				// nothing else to do with directories
				return nil
			} else {
				ext := filepath.Ext(d.Name())
				name := strings.TrimSuffix(d.Name(), ext)

				var imageData = ImageData{
					path: path,
					name: name,
				}

				images <- &imageData
			}

			return nil
		})
	}()

	return images, errc
}

func processor(i int, images <-chan *ImageData, results chan<- *ImageData) {
	for image := range images {
		logger.Printf("%d - %s", i, image.path)

		processImage(image)
		results <- image
	}
}

func processImage(imageData *ImageData) {
	jpgExportParams := &vips.JpegExportParams{
		StripMetadata:      true,
		Quality:            75,
		Interlace:          true,
		OptimizeCoding:     true,
		SubsampleMode:      vips.VipsForeignSubsampleAuto,
		TrellisQuant:       true,
		OvershootDeringing: true,
		OptimizeScans:      true,
		QuantTable:         3,
	}

	dir := filepath.Dir(imageData.path)

	image, err := vips.NewImageFromFile(imageData.path)
	defer image.Close()
	if err != nil {
		panic(err)
	}

	// png is nice but way too big
	if filepath.Ext(imageData.path) == ".png" {
		logger.Printf("Retyping image to jpg: %s", imageData.path)

		err := convertToJPG(imageData, image, jpgExportParams)
		if err != nil {
			panic(err)
		}
	}

	ext := ".jpg"
	imageData.ThumbPath = filepath.Join(dir, imageData.name+"-thumbnail"+ext)
	imageData.DisplayPath = filepath.Join(dir, imageData.name+"-display"+ext)
	imageData.FullPath = filepath.Join(dir, imageData.name+ext)

	// these get updated if a lower-res slide image is generated
	imageData.Height = image.Height()
	imageData.Width = image.Width()

	// these are for the deepzoom plugin
	imageData.MaxHeight = image.Height()
	imageData.MaxWidth = image.Width()

	var wg sync.WaitGroup

	// the grid thumbnail
	go generateThumbnail(&wg, imageData, jpgExportParams)

	// the slide image
	if image.Width() > slideHeight || image.Height() > slideHeight {
		go generateSlideImage(&wg, imageData, jpgExportParams)
	}

	// generate tiles if necessary
	if image.Width() > tileMinDimension || image.Height() > tileMinDimension {
		go generateImageTiles(&wg, imageData)
	}

	wg.Wait()
}

func convertToJPG(imageData *ImageData, image *vips.ImageRef, jpegExportParams *vips.JpegExportParams) error {
	ext := ".jpg"
	// vips image to jpg
	jpgFile := fmt.Sprintf("%s%s", imageData.name, ext)
	path := filepath.Join(imageData.FullPath, jpgFile)

	// for web viewing/consistency with generated tiles
	err := image.ToColorSpace(vips.InterpretationSRGB)
	if err != nil {
		return err
	}

	jpgImageBytes, _, err := image.ExportJpeg(jpegExportParams)
	if err != nil {
		return err
	}

	err = os.WriteFile(path, jpgImageBytes, 0644)
	if err != nil {
		return err
	}
	return nil
}

func generateThumbnail(wg *sync.WaitGroup, imageData *ImageData, jpgExportParams *vips.JpegExportParams) error {
	wg.Add(1)
	defer wg.Done()

	thumbnail, err := vips.NewThumbnailFromFile(imageData.FullPath, math.MaxInt16, thumbnailHeight, vips.InterestingNone)
	if err != nil {
		return err
	}
	defer thumbnail.Close()

	thumbnailBytes, _, err := thumbnail.ExportJpeg(jpgExportParams)
	if err != nil {
		return err
	}
	err = os.WriteFile(imageData.ThumbPath, thumbnailBytes, 0644)
	if err != nil {
		return err
	}

	return nil
}

func generateSlideImage(wg *sync.WaitGroup, imageData *ImageData, jpgExportParams *vips.JpegExportParams) error {
	wg.Add(1)
	defer wg.Done()

	display, err := vips.NewThumbnailFromFile(imageData.FullPath, math.MaxInt16, slideHeight, vips.InterestingNone)
	if err != nil {
		return err
	}
	defer display.Close()

	displayBytes, _, err := display.ExportJpeg(jpgExportParams)
	if err != nil {
		return err
	}

	err = os.WriteFile(imageData.DisplayPath, displayBytes, 0644)
	if err != nil {
		return err
	}

	imageData.Height = display.Height()
	imageData.Width = display.Width()
	return nil
}

func generateImageTiles(wg *sync.WaitGroup, imageData *ImageData) {
	wg.Add(1)
	defer wg.Done()

	logger.Printf("Generating tiles for %s", imageData.path)

	// Shell out because govips doesn't have a dzsave binding
	imageBaseDir := filepath.Join(filepath.Dir(imageData.path), imageData.name)
	vipsDzCmd := exec.Command("vips", "dzsave", imageData.path, imageBaseDir, "--centre")
	err := vipsDzCmd.Run()
	if err != nil {
		panic(err)
	}

	imageData.Tiles = imageBaseDir + "_files"

	// delete the unnecessary generated meta files
	err = os.Remove(imageBaseDir + ".dzi")
	if err != nil {
		logger.Println(err)
	}
}

func writeDirImageData(dir string, imageData map[string]*ImageData) {
	logger.Printf("Saving JSON to %s/images.json", dir)

	logger.Printf("Opening JSON file %s", dir)
	jsonFile, err := os.Create(filepath.Join(dir, "images.json"))

	defer func() {
		logger.Printf("Closing JSON file for %s", dir)
		err := jsonFile.Close()
		if err != nil {
			logger.Println(err)
			return
		}
	}()

	imageJson, err := json.MarshalIndent(imageData, "", "  ")
	if err != nil {
		panic(err)
	}
	_, err = jsonFile.Write(imageJson)
	if err != nil {
		panic(err)
	}
}
