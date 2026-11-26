# Stands in for terraform in the run plan / run apply replay cases: logs the step
# for the log pump and the terminal. $FAKE_FAIL names the step that exits 3;
# $FAKE_GARBLE makes the plan step write a plan file that is not JSON.
step=$1
stack=$(basename "$PWD")
echo "$step $stack" >> tfstackplan.log
echo "$step $stack"
if [ "$step" = plan ] && [ -n "$FAKE_GARBLE" ]; then echo '{' > tfplan.json; fi
if [ "$FAKE_FAIL" = "$step" ]; then exit 3; fi
exit 0
