package input

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Jeffail/benthos/v3/lib/log"
	"github.com/Jeffail/benthos/v3/lib/metrics"
	"github.com/Jeffail/benthos/v3/lib/response"
	"github.com/Jeffail/benthos/v3/lib/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFiles(t *testing.T, dir string, nameToContent map[string]string) {
	t.Helper()

	for k, v := range nameToContent {
		require.NoError(t, os.WriteFile(filepath.Join(dir, k), []byte(v), 0600))
	}
}

func TestSequenceHappy(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "benthos_sequence_input_test")
	require.NoError(t, err)

	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	files := map[string]string{
		"f1": "foo\nbar\nbaz",
		"f2": "buz\nbev\nbif\n",
		"f3": "qux\nquz\nqev",
	}

	writeFiles(t, tmpDir, files)

	conf := NewConfig()
	conf.Type = TypeSequence

	for _, k := range []string{"f1", "f2", "f3"} {
		inConf := NewConfig()
		inConf.Type = TypeFile
		inConf.File.Path = filepath.Join(tmpDir, k)
		conf.Sequence.Inputs = append(conf.Sequence.Inputs, inConf)
	}

	rdr, err := New(conf, types.NoopMgr(), log.Noop(), metrics.Noop())
	require.NoError(t, err)

	exp, act := []string{
		"foo", "bar", "baz", "buz", "bev", "bif", "qux", "quz", "qev",
	}, []string{}

consumeLoop:
	for {
		select {
		case tran, open := <-rdr.TransactionChan():
			if !open {
				break consumeLoop
			}
			assert.Equal(t, 1, tran.Payload.Len())
			act = append(act, string(tran.Payload.Get(0).Get()))
			select {
			case tran.ResponseChan <- response.NewAck():
			case <-time.After(time.Minute):
				t.Fatalf("failed to ack after: %v", act)
			}
		case <-time.After(time.Minute):
			t.Fatalf("Failed to consume message after: %v", act)
		}
	}

	assert.Equal(t, exp, act)

	rdr.CloseAsync()
	assert.NoError(t, rdr.WaitForClose(time.Second))
}

func TestSequenceJoins(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "benthos_sequence_joins_test")
	require.NoError(t, err)

	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	files := map[string]string{
		"csv1": "id,name,age\naaa,A,20\nbbb,B,21\nccc,B,22\n",
		"csv2": "id,hobby\nccc,fencing\naaa,running\naaa,gaming\n",
		"ndjson1": `{"id":"aaa","stuff":{"first":"foo"}}
{"id":"bbb","stuff":{"first":"bar"}}
{"id":"aaa","stuff":{"second":"baz"}}`,
	}

	writeFiles(t, tmpDir, files)

	conf := NewConfig()
	conf.Type = TypeSequence
	conf.Sequence.ShardedJoin.IDPath = "id"
	conf.Sequence.ShardedJoin.Iterations = 1
	conf.Sequence.ShardedJoin.Type = "full-outter"

	csvConf := NewConfig()
	csvConf.Type = TypeCSVFile
	csvConf.CSVFile.Paths = []string{
		filepath.Join(tmpDir, "csv1"),
		filepath.Join(tmpDir, "csv2"),
	}
	conf.Sequence.Inputs = append(conf.Sequence.Inputs, csvConf)
	for _, k := range []string{"ndjson1"} {
		inConf := NewConfig()
		inConf.Type = TypeFile
		inConf.File.Path = filepath.Join(tmpDir, k)
		conf.Sequence.Inputs = append(conf.Sequence.Inputs, inConf)
	}

	rdr, err := New(conf, types.NoopMgr(), log.Noop(), metrics.Noop())
	require.NoError(t, err)

	exp, act := []string{
		`{"age":"20","hobby":["running","gaming"],"id":"aaa","name":"A","stuff":{"first":"foo","second":"baz"}}`,
		`{"age":"21","id":"bbb","name":"B","stuff":{"first":"bar"}}`,
		`{"age":"22","hobby":"fencing","id":"ccc","name":"B"}`,
	}, []string{}

consumeLoop:
	for {
		select {
		case tran, open := <-rdr.TransactionChan():
			if !open {
				break consumeLoop
			}
			assert.Equal(t, 1, tran.Payload.Len())
			act = append(act, string(tran.Payload.Get(0).Get()))
			select {
			case tran.ResponseChan <- response.NewAck():
			case <-time.After(time.Minute):
				t.Fatalf("failed to ack after: %v", act)
			}
		case <-time.After(time.Minute):
			t.Fatalf("Failed to consume message after: %v", act)
		}
	}

	sort.Strings(exp)
	sort.Strings(act)
	assert.Equal(t, exp, act)

	rdr.CloseAsync()
	assert.NoError(t, rdr.WaitForClose(time.Second))
}

func TestSequenceJoinsMergeStrategies(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		flushOnFinal bool
		mergeStrat   string
		files        map[string]string
		finalFile    string
		result       []string
	}{
		{
			name:         "array from final",
			flushOnFinal: true,
			mergeStrat:   "array",
			files: map[string]string{
				"csv1": "id,name,age\naaa,A,20\nbbb,B,21\nccc,B,22\n",
				"csv2": "id,hobby\nccc,fencing\naaa,running\naaa,gaming\n",
			},
			finalFile: "id,stuff\naaa,first\nccc,second\naaa,third\n",
			result: []string{
				`{"age":"20","hobby":["running","gaming"],"id":"aaa","name":"A","stuff":"first"}`,
				`{"age":"22","hobby":"fencing","id":"ccc","name":"B","stuff":"second"}`,
				`{"age":"20","hobby":["running","gaming"],"id":"aaa","name":"A","stuff":["first","third"]}`,
			},
		},
		{
			name:         "replace from final",
			flushOnFinal: true,
			mergeStrat:   "replace",
			files: map[string]string{
				"csv1": "id,name,age\naaa,A,20\nbbb,B,21\nccc,B,22\n",
				"csv2": "id,hobby\nccc,fencing\naaa,running\naaa,gaming\n",
			},
			finalFile: "id,stuff\naaa,first\nccc,second\naaa,third\n",
			result: []string{
				`{"age":"20","hobby":"gaming","id":"aaa","name":"A","stuff":"first"}`,
				`{"age":"20","hobby":"gaming","id":"aaa","name":"A","stuff":"third"}`,
				`{"age":"22","hobby":"fencing","id":"ccc","name":"B","stuff":"second"}`,
			},
		},
		{
			name:         "keep from final",
			flushOnFinal: true,
			mergeStrat:   "keep",
			files: map[string]string{
				"csv1": "id,name,age\naaa,A,20\nbbb,B,21\nccc,B,22\n",
				"csv2": "id,hobby\nccc,fencing\naaa,running\naaa,gaming\n",
			},
			finalFile: "id,stuff\naaa,first\nccc,second\naaa,third\n",
			result: []string{
				`{"age":"20","hobby":"running","id":"aaa","name":"A","stuff":"first"}`,
				`{"age":"20","hobby":"running","id":"aaa","name":"A","stuff":"first"}`,
				`{"age":"22","hobby":"fencing","id":"ccc","name":"B","stuff":"second"}`,
			},
		},
	}

	for _, test := range testCases {
		test := test
		t.Run(test.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "benthos_sequence_joins_test")
			require.NoError(t, err)

			t.Cleanup(func() { os.RemoveAll(tmpDir) })

			writeFiles(t, tmpDir, test.files)
			writeFiles(t, tmpDir, map[string]string{
				"final.csv": test.finalFile,
			})

			conf := NewConfig()
			conf.Type = TypeSequence
			conf.Sequence.ShardedJoin.IDPath = "id"
			conf.Sequence.ShardedJoin.MergeStrategy = test.mergeStrat
			if test.flushOnFinal {
				conf.Sequence.ShardedJoin.Type = "outter"
			} else {
				conf.Sequence.ShardedJoin.Type = "full-outter"
			}
			conf.Sequence.ShardedJoin.Iterations = 1

			csvConf := NewConfig()
			csvConf.Type = TypeCSVFile
			for k := range test.files {
				csvConf.CSVFile.Paths = append(csvConf.CSVFile.Paths, filepath.Join(tmpDir, k))
			}
			conf.Sequence.Inputs = append(conf.Sequence.Inputs, csvConf)

			finalConf := NewConfig()
			finalConf.Type = TypeCSVFile
			finalConf.CSVFile.Paths = []string{filepath.Join(tmpDir, "final.csv")}
			conf.Sequence.Inputs = append(conf.Sequence.Inputs, finalConf)

			rdr, err := New(conf, types.NoopMgr(), log.Noop(), metrics.Noop())
			require.NoError(t, err)

			exp, act := test.result, []string{}

		consumeLoop:
			for {
				select {
				case tran, open := <-rdr.TransactionChan():
					if !open {
						break consumeLoop
					}
					assert.Equal(t, 1, tran.Payload.Len())
					act = append(act, string(tran.Payload.Get(0).Get()))
					select {
					case tran.ResponseChan <- response.NewAck():
					case <-time.After(time.Minute):
						t.Fatalf("failed to ack after: %v", act)
					}
				case <-time.After(time.Minute):
					t.Fatalf("Failed to consume message after: %v", act)
				}
			}

			sort.Strings(exp)
			sort.Strings(act)
			assert.Equal(t, exp, act)

			rdr.CloseAsync()
			assert.NoError(t, rdr.WaitForClose(time.Second))
		})
	}
}

func TestSequenceJoinsBig(t *testing.T) {
	t.Skip()
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "benthos_sequence_joins_big_test")
	require.NoError(t, err)

	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	jsonPath := filepath.Join(tmpDir, "one.ndjson")
	csvPath := filepath.Join(tmpDir, "two.csv")

	ndjsonFile, err := os.Create(jsonPath)
	require.NoError(t, err)

	csvFile, err := os.Create(csvPath)
	require.NoError(t, err)

	conf := NewConfig()
	conf.Type = TypeSequence
	conf.Sequence.ShardedJoin.IDPath = "id"
	conf.Sequence.ShardedJoin.Iterations = 5
	conf.Sequence.ShardedJoin.Type = "full-outter"

	csvConf := NewConfig()
	csvConf.Type = TypeCSVFile
	csvConf.CSVFile.Paths = []string{csvPath}
	conf.Sequence.Inputs = append(conf.Sequence.Inputs, csvConf)

	jsonConf := NewConfig()
	jsonConf.Type = TypeFile
	jsonConf.File.Paths = []string{jsonPath}
	jsonConf.File.Codec = "lines"
	conf.Sequence.Inputs = append(conf.Sequence.Inputs, jsonConf)

	totalRows := 1000

	exp, act := []string{}, []string{}

	_, err = csvFile.Write([]byte("id,bar\n"))
	require.NoError(t, err)
	for i := 0; i < totalRows; i++ {
		exp = append(exp, fmt.Sprintf(`{"bar":["bar%v","baz%v"],"foo":"foo%v","id":"%v"}`, i, i, i, i))

		_, err = fmt.Fprintf(ndjsonFile, "{\"id\":\"%v\",\"foo\":\"foo%v\"}\n", i, i)
		require.NoError(t, err)

		_, err = fmt.Fprintf(csvFile, "%v,bar%v\n", i, i)
		require.NoError(t, err)
	}
	for i := 0; i < totalRows; i++ {
		_, err = fmt.Fprintf(csvFile, "%v,baz%v\n", i, i)
		require.NoError(t, err)
	}
	require.NoError(t, ndjsonFile.Close())
	require.NoError(t, csvFile.Close())

	rdr, err := New(conf, types.NoopMgr(), log.Noop(), metrics.Noop())
	require.NoError(t, err)

consumeLoop:
	for {
		select {
		case tran, open := <-rdr.TransactionChan():
			if !open {
				break consumeLoop
			}
			assert.Equal(t, 1, tran.Payload.Len())
			act = append(act, string(tran.Payload.Get(0).Get()))
			select {
			case tran.ResponseChan <- response.NewAck():
			case <-time.After(time.Minute):
				t.Fatalf("failed to ack after: %v", act)
			}
		case <-time.After(time.Minute):
			t.Fatalf("Failed to consume message after: %v", act)
		}
	}

	sort.Strings(exp)
	sort.Strings(act)
	assert.Equal(t, exp, act)

	rdr.CloseAsync()
	assert.NoError(t, rdr.WaitForClose(time.Second))
}

func TestSequenceSad(t *testing.T) {
	t.Parallel()

	tmpDir, err := os.MkdirTemp("", "benthos_sequence_input_test")
	require.NoError(t, err)

	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	files := map[string]string{
		"f1": "foo\nbar\nbaz",
		"f4": "buz\nbev\nbif\n",
	}

	writeFiles(t, tmpDir, files)

	conf := NewConfig()
	conf.Type = TypeSequence

	for _, k := range []string{"f1", "f2", "f3"} {
		inConf := NewConfig()
		inConf.Type = TypeFile
		inConf.File.Path = filepath.Join(tmpDir, k)
		conf.Sequence.Inputs = append(conf.Sequence.Inputs, inConf)
	}

	rdr, err := New(conf, types.NoopMgr(), log.Noop(), metrics.Noop())
	require.NoError(t, err)

	exp := []string{
		"foo", "bar", "baz",
	}

	for i, str := range exp {
		select {
		case tran, open := <-rdr.TransactionChan():
			if !open {
				t.Fatal("closed earlier than expected")
			}
			assert.Equal(t, 1, tran.Payload.Len())
			assert.Equal(t, str, string(tran.Payload.Get(0).Get()))
			select {
			case tran.ResponseChan <- response.NewAck():
			case <-time.After(time.Minute):
				t.Fatalf("failed to ack after: %v", str)
			}
		case <-time.After(time.Minute):
			t.Fatalf("Failed to consume message %v", i)
		}
	}

	select {
	case <-rdr.TransactionChan():
		t.Fatal("unexpected transaction")
	case <-time.After(100 * time.Millisecond):
	}

	exp = []string{
		"buz", "bev", "bif",
	}

	require.NoError(t, os.Rename(filepath.Join(tmpDir, "f4"), filepath.Join(tmpDir, "f2")))

	for i, str := range exp {
		select {
		case tran, open := <-rdr.TransactionChan():
			if !open {
				t.Fatal("closed earlier than expected")
			}
			assert.Equal(t, 1, tran.Payload.Len())
			assert.Equal(t, str, string(tran.Payload.Get(0).Get()))
			select {
			case tran.ResponseChan <- response.NewAck():
			case <-time.After(time.Minute):
				t.Fatalf("failed to ack after: %v", str)
			}
		case <-time.After(time.Minute):
			t.Fatalf("Failed to consume message %v", i)
		}
	}

	rdr.CloseAsync()
	assert.NoError(t, rdr.WaitForClose(time.Second))
}
